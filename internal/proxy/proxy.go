// Package proxy implements the core reverse-proxy handler for LLM APIs.
package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/nhm0819/llm-proxy/internal/audit"
	"github.com/nhm0819/llm-proxy/internal/config"
	"github.com/nhm0819/llm-proxy/internal/loki"
	"github.com/nhm0819/llm-proxy/internal/metrics"
	"github.com/nhm0819/llm-proxy/internal/pii"
	"github.com/nhm0819/llm-proxy/internal/quota"
	"github.com/nhm0819/llm-proxy/internal/ratelimit"
	"github.com/nhm0819/llm-proxy/internal/router"
	"github.com/nhm0819/llm-proxy/internal/tokencount"
	"github.com/nhm0819/llm-proxy/pkg/httputil"
)

const (
	headerRequestID  = "X-Request-ID"
	headerTokenLimit = "X-Token-Limit"
	headerTokenUsed  = "X-Token-Used"
	headerTokenRem   = "X-Token-Remaining"
)

// Dependencies bundles all external collaborators for the proxy handler.
// Using a struct instead of individual fields makes the proxy easy to test by
// swapping out individual dependencies.
type Dependencies struct {
	Router      *router.Router
	PIIScanner  *pii.Scanner
	TokenCount  tokencount.Counter
	QuotaStore  *quota.Store
	RLStore     *ratelimit.Store
	AuditStore  *audit.Store
	LokiPusher  *loki.Pusher
	Metrics     *metrics.Metrics // may be nil (metrics disabled)
	HTTPClient  *http.Client
}

// Handler is the core reverse-proxy HTTP handler.
type Handler struct {
	cfg      config.Config
	deps     Dependencies
	quotaLoc *time.Location
}

// New returns a ready-to-use proxy Handler.
func New(cfg config.Config, deps Dependencies) *Handler {
	loc, err := time.LoadLocation(cfg.QuotaTimezone)
	if err != nil {
		loc = time.UTC
	}
	return &Handler{cfg: cfg, deps: deps, quotaLoc: loc}
}

// ServeHTTP is the main entry-point.  It enforces rate limits, PII policy and
// token quotas before forwarding the request to the upstream LLM API and
// logging the outcome.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	reqID := r.Header.Get(headerRequestID)
	if reqID == "" {
		reqID = httputil.NewRequestID()
	}
	w.Header().Set(headerRequestID, reqID)

	kind := ClassifyPath(r.URL.Path)

	// ── 1. Read & parse request body ────────────────────────────────────────
	bodyBytes, bodyJSON, err := h.readBody(r)
	if err != nil {
		h.errResp(w, r, reqID, kind, "", config.RouteRule{Name: "none"}, start,
			http.StatusBadRequest, "invalid_request_error", "failed to read request body", err.Error())
		return
	}

	model := ExtractModel(bodyJSON)
	userID := ExtractUserID(r, bodyJSON)
	if userID == "" {
		userID = "anonymous"
	}
	stream := getBool(bodyJSON, "stream")
	route := h.deps.Router.Pick(model)

	// ── 2. RPS rate limit ────────────────────────────────────────────────────
	rpsLimit := h.rpsLimitFor(userID)
	rpsRes, rpsErr := h.deps.RLStore.Allow(r.Context(), userID, rpsLimit)
	if rpsErr != nil {
		h.errResp(w, r, reqID, kind, model, route, start,
			http.StatusBadGateway, "server_error", "rate limit backend error", rpsErr.Error())
		return
	}
	if !rpsRes.OK {
		if h.deps.Metrics != nil {
			h.deps.Metrics.RecordRPSRejection(userID)
		}
		h.errResp(w, r, reqID, kind, model, route, start,
			http.StatusTooManyRequests, "rate_limit_error", "rps rate limit exceeded",
			fmt.Sprintf("rps limit=%d count=%d", rpsLimit, rpsRes.Count))
		return
	}

	// ── 3. PII scan ──────────────────────────────────────────────────────────
	reqText := CollectRequestText(kind, bodyJSON)
	reqPII := h.deps.PIIScanner.Redact(reqText)
	reqHash := h.hashText(reqText)

	if reqPII.Found() {
		if h.deps.Metrics != nil {
			h.deps.Metrics.RecordPII(string(kind), reqPII.Types, "request")
		}
		if h.cfg.PIIBlock {
			h.errResp(w, r, reqID, kind, model, route, start,
				http.StatusBadRequest, "invalid_request_error", "PII detected in request (blocked by policy)",
				"PII blocked")
			return
		}
	}

	// ── 4. Token quota reservation ───────────────────────────────────────────
	now := time.Now().In(h.quotaLoc)
	qDayKey := quota.DayKey(userID, now)
	ttlSec := quota.SecondsUntilMidnight(now)
	tokenLimit := h.tokenLimitFor(userID)

	var promptEst, reserve, usedAfterReserve int

	if IsGenerative(kind) && model != "" && bodyJSON != nil {
		promptEst = h.deps.TokenCount.Count(model, reqText)
		if msgs, ok := bodyJSON["messages"].([]any); ok {
			promptEst += CountImageTokens(msgs)
		}
		maxOut := ExtractMaxOutputTokens(kind, bodyJSON, h.cfg)
		reserve = int(math.Ceil(float64(promptEst)*h.cfg.TokenSafetyFactor)) + maxOut

		qRes, err := h.deps.QuotaStore.Reserve(r.Context(), qDayKey, tokenLimit, reserve, ttlSec)
		if err != nil {
			h.errResp(w, r, reqID, kind, model, route, start,
				http.StatusBadGateway, "server_error", "quota backend error", err.Error())
			return
		}
		if !qRes.OK {
			if h.deps.Metrics != nil {
				h.deps.Metrics.RecordQuotaRejection(userID)
			}
			remaining := qRes.Limit - qRes.Used
			setQuotaHeaders(w, qRes.Limit, qRes.Used, remaining)
			h.errResp(w, r, reqID, kind, model, route, start,
				http.StatusTooManyRequests, "rate_limit_error", "token quota exceeded", "quota exceeded")
			return
		}
		usedAfterReserve = qRes.Used
		setQuotaHeaders(w, qRes.Limit, qRes.Used, qRes.Limit-qRes.Used)
	}

	// ── 5. Build & send upstream request ────────────────────────────────────
	finalBody := h.maybeInjectUsage(kind, route, bodyJSON, bodyBytes, stream)

	upURL := strings.TrimRight(route.BaseURL, "/") + r.URL.Path
	if r.URL.RawQuery != "" {
		upURL += "?" + r.URL.RawQuery
	}

	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upURL, bytes.NewReader(finalBody))
	if err != nil {
		h.refundQuota(r.Context(), qDayKey, reserve, ttlSec)
		httputil.RespondErr(w, http.StatusInternalServerError, "server_error", "failed to build upstream request")
		return
	}
	httputil.CopyHeaders(upReq.Header, r.Header)
	upReq.Header.Set("Accept-Encoding", "identity")
	upReq.Header.Set(headerRequestID, reqID)
	if route.APIKey != "" {
		upReq.Header.Set("Authorization", "Bearer "+route.APIKey)
	}

	resp, err := h.deps.HTTPClient.Do(upReq)
	if err != nil {
		h.refundQuota(r.Context(), qDayKey, reserve, ttlSec)
		entry := h.baseEntry(reqID, userID, kind, model, route.Name, r, start)
		entry.PIIFound = reqPII.Found()
		entry.PIITypes = nonZeroOnly(reqPII.Types)
		entry.RequestExcerpt = truncateBytes(reqPII.Text, config.DefaultMaxLoggedTextBytes)
		entry.RequestHash = reqHash
		entry.PromptTokensEst = promptEst
		entry.ReserveTokens = reserve
		entry.TokenLimit = tokenLimit
		entry.TokenUsed = usedAfterReserve
		entry.TokenRemaining = tokenLimit - usedAfterReserve
		entry.RPSLimit = rpsLimit
		entry.RPSCount = rpsRes.Count
		entry.Status = http.StatusBadGateway
		entry.DurationMs = time.Since(start).Milliseconds()
		entry.ErrorMessage = err.Error()
		h.logAndAudit(entry, route.Name, reqHash, "", 0, reqPII.Found())
		httputil.RespondErr(w, http.StatusBadGateway, "upstream_error", "upstream request failed")
		return
	}
	defer resp.Body.Close()

	httputil.CopyHeaders(w.Header(), resp.Header)
	w.Header().Set(headerRequestID, reqID)

	// ── 6. Response handling ─────────────────────────────────────────────────
	if stream {
		h.handleStream(w, r, resp, reqID, userID, kind, model, route, start,
			reqPII, reqHash, promptEst, reserve, usedAfterReserve, tokenLimit,
			rpsLimit, rpsRes.Count, qDayKey, ttlSec)
		return
	}
	h.handleNonStream(w, r, resp, reqID, userID, kind, model, route, start,
		reqPII, reqHash, promptEst, reserve, usedAfterReserve, tokenLimit,
		rpsLimit, rpsRes.Count, qDayKey, ttlSec)
}

// ── Stream response ──────────────────────────────────────────────────────────

func (h *Handler) handleStream(
	w http.ResponseWriter, r *http.Request, resp *http.Response,
	reqID, userID string, kind Kind, model string, route config.RouteRule,
	start time.Time, reqPII pii.Result, reqHash string,
	promptEst, reserve, usedAfterReserve, tokenLimit,
	rpsLimit, rpsCount int, qDayKey string, ttlSec int,
) {
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)

	respHash := audit.NewHashBuilder(h.cfg.AuditHMACKey)
	var excerptBuf strings.Builder
	excerptBuf.Grow(config.DefaultMaxLoggedTextBytes)

	piiFoundResp := false

	parser := NewStreamEventParser(kind, respHash, &excerptBuf, config.DefaultMaxLoggedTextBytes,
		func(delta string) {
			result := h.deps.PIIScanner.Redact(delta)
			if result.Found() {
				piiFoundResp = true
			}
		},
	)

	_ = StreamSSE(resp.Body, StreamCallbacks{
		OnRawLine: func(line string) {
			_, _ = io.WriteString(w, line)
			if flusher != nil {
				flusher.Flush()
			}
		},
		OnEvent: parser.Parse,
	})

	actualTotal := parser.ActualTotalTokens
	if actualTotal <= 0 && IsGenerative(kind) && model != "" {
		estOut := int(math.Ceil(float64(parser.OutputCharCount) / 4.0))
		actualTotal = promptEst + estOut
	}

	// Quota adjustment (best-effort; use background context since request may be done)
	if reserve > 0 && actualTotal > 0 {
		delta := actualTotal - reserve
		_, _ = h.deps.QuotaStore.Adjust(context.Background(), qDayKey, delta, ttlSec)
	}

	respExcerptRedacted := h.deps.PIIScanner.Redact(excerptBuf.String())

	entry := h.baseEntry(reqID, userID, kind, model, route.Name, r, start)
	entry.Status = resp.StatusCode
	entry.DurationMs = time.Since(start).Milliseconds()
	entry.PIIFound = reqPII.Found() || piiFoundResp
	entry.PIITypes = nonZeroOnly(reqPII.Types)
	entry.RequestExcerpt = truncateBytes(reqPII.Text, config.DefaultMaxLoggedTextBytes)
	entry.ResponseExcerpt = truncateBytes(respExcerptRedacted.Text, config.DefaultMaxLoggedTextBytes)
	entry.RequestHash = reqHash
	entry.ResponseHash = respHash.SumHex()
	entry.PromptTokensEst = promptEst
	entry.ReserveTokens = reserve
	entry.ActualTotalTokens = actualTotal
	entry.TokenLimit = tokenLimit
	entry.TokenUsed = usedAfterReserve
	entry.TokenRemaining = tokenLimit - usedAfterReserve
	entry.RPSLimit = rpsLimit
	entry.RPSCount = rpsCount

	h.logAndAudit(entry, route.Name, reqHash, respHash.SumHex(), actualTotal, entry.PIIFound)
}

// ── Non-stream response ──────────────────────────────────────────────────────

func (h *Handler) handleNonStream(
	w http.ResponseWriter, r *http.Request, resp *http.Response,
	reqID, userID string, kind Kind, model string, route config.RouteRule,
	start time.Time, reqPII pii.Result, reqHash string,
	promptEst, reserve, usedAfterReserve, tokenLimit,
	rpsLimit, rpsCount int, qDayKey string, ttlSec int,
) {
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, h.cfg.MaxBodyBytes))

	var respJSON map[string]any
	if strings.Contains(resp.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(respBytes, &respJSON)
	}

	respText := CollectResponseText(kind, respJSON)
	respPII := h.deps.PIIScanner.Redact(respText)
	respHash := h.hashText(respText)

	piiFound := reqPII.Found() || respPII.Found()
	mergedPIITypes := mergePIITypes(reqPII.Types, respPII.Types)

	actualTotal := ExtractTotalTokens(respJSON)
	if actualTotal <= 0 && IsGenerative(kind) && model != "" {
		actualTotal = promptEst + h.deps.TokenCount.Count(model, respText)
	}

	// Adjust quota and update headers BEFORE writing the response so that the
	// corrected token counts are visible to the client.
	if reserve > 0 && actualTotal > 0 {
		delta := actualTotal - reserve
		newUsed, err := h.deps.QuotaStore.Adjust(r.Context(), qDayKey, delta, ttlSec)
		if err == nil {
			usedAfterReserve = newUsed
			setQuotaHeaders(w, tokenLimit, newUsed, tokenLimit-newUsed)
		}
	}

	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(respBytes)

	entry := h.baseEntry(reqID, userID, kind, model, route.Name, r, start)
	entry.Status = resp.StatusCode
	entry.DurationMs = time.Since(start).Milliseconds()
	entry.PIIFound = piiFound
	entry.PIITypes = nonZeroOnly(mergedPIITypes)
	entry.RequestExcerpt = truncateBytes(reqPII.Text, config.DefaultMaxLoggedTextBytes)
	entry.ResponseExcerpt = truncateBytes(respPII.Text, config.DefaultMaxLoggedTextBytes)
	entry.RequestHash = reqHash
	entry.ResponseHash = respHash
	entry.PromptTokensEst = promptEst
	entry.ReserveTokens = reserve
	entry.ActualTotalTokens = actualTotal
	entry.TokenLimit = tokenLimit
	entry.TokenUsed = usedAfterReserve
	entry.TokenRemaining = tokenLimit - usedAfterReserve
	entry.RPSLimit = rpsLimit
	entry.RPSCount = rpsCount

	h.logAndAudit(entry, route.Name, reqHash, respHash, actualTotal, piiFound)
}

// ── Internal helpers ─────────────────────────────────────────────────────────

func (h *Handler) readBody(r *http.Request) ([]byte, map[string]any, error) {
	if r.Body == nil {
		return nil, nil, nil
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut && r.Method != http.MethodPatch {
		return nil, nil, nil
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, h.cfg.MaxBodyBytes))
	r.Body.Close()
	if err != nil {
		return nil, nil, err
	}
	var bodyJSON map[string]any
	if len(b) > 0 && strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(b, &bodyJSON)
	}
	return b, bodyJSON, nil
}

func (h *Handler) maybeInjectUsage(kind Kind, route config.RouteRule, body map[string]any, raw []byte, stream bool) []byte {
	if !h.cfg.InjectChatStreamUsage || !stream || body == nil {
		return raw
	}
	if kind != KindChat && kind != KindCompletions {
		return raw
	}
	if h.cfg.InjectChatStreamUsageOpenAI && !strings.Contains(route.BaseURL, "api.openai.com") {
		return raw
	}
	if so, ok := body["stream_options"].(map[string]any); ok {
		if v, _ := so["include_usage"].(bool); v {
			return raw // already set
		}
	}
	so, _ := body["stream_options"].(map[string]any)
	if so == nil {
		so = map[string]any{}
	}
	so["include_usage"] = true
	body["stream_options"] = so
	b, _ := json.Marshal(body)
	return b
}

func (h *Handler) refundQuota(ctx context.Context, qDayKey string, reserve, ttlSec int) {
	if reserve <= 0 {
		return
	}
	_, _ = h.deps.QuotaStore.Adjust(ctx, qDayKey, -reserve, ttlSec)
}

func (h *Handler) hashText(text string) string {
	hb := audit.NewHashBuilder(h.cfg.AuditHMACKey)
	hb.WriteString(text)
	return hb.SumHex()
}

func (h *Handler) tokenLimitFor(userID string) int {
	if v, ok := h.cfg.UserDailyTokenLimitOverride[userID]; ok && v > 0 {
		return v
	}
	return h.cfg.DailyTokenLimit
}

func (h *Handler) rpsLimitFor(userID string) int {
	if v, ok := h.cfg.UserRateLimitOverrideRPS[userID]; ok && v > 0 {
		return v
	}
	return h.cfg.RateLimitRPS
}

func (h *Handler) baseEntry(reqID, userID string, kind Kind, model, upstream string, r *http.Request, _ time.Time) loki.Entry {
	return loki.Entry{
		Time:      time.Now().Format(time.RFC3339Nano),
		RequestID: reqID,
		UserID:    userID,
		Path:      r.URL.Path,
		Method:    r.Method,
		Kind:      string(kind),
		Model:     model,
		Upstream:  upstream,
	}
}

func (h *Handler) logAndAudit(entry loki.Entry, upstreamName, reqHash, respHash string, totalTokens int, piiFound bool) {
	log.Printf("proxy_log %s", mustJSON(entry))

	// ── Prometheus metrics ────────────────────────────────────────────────────
	if m := h.deps.Metrics; m != nil {
		m.RecordRequest(entry.Status, entry.Kind, entry.Upstream, entry.UserID,
			float64(entry.DurationMs)/1000.0)
		if totalTokens > 0 {
			m.RecordTokens(entry.UserID, entry.Kind, entry.Model, totalTokens)
		}
	}

	// ── Loki ─────────────────────────────────────────────────────────────────
	if h.cfg.LokiPushURL != "" {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = h.deps.LokiPusher.Push(ctx, entry, map[string]string{"upstream": upstreamName})
		}()
	}

	// ── Audit ─────────────────────────────────────────────────────────────────
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = h.deps.AuditStore.Write(ctx, audit.Record{
			RequestID:   entry.RequestID,
			UserID:      entry.UserID,
			Kind:        entry.Kind,
			Model:       entry.Model,
			Upstream:    entry.Upstream,
			Status:      entry.Status,
			ReqHash:     reqHash,
			RespHash:    respHash,
			TotalTokens: totalTokens,
			PIIFound:    piiFound,
		})
	}()
}

func (h *Handler) errResp(
	w http.ResponseWriter, r *http.Request,
	reqID string, kind Kind, model string, route config.RouteRule,
	start time.Time, status int, typ, msg, errMsg string,
) {
	userID := r.Header.Get("X-User-ID")
	if userID == "" {
		userID = "unknown"
	}
	entry := h.baseEntry(reqID, userID, kind, model, route.Name, r, start)
	entry.Status = status
	entry.DurationMs = time.Since(start).Milliseconds()
	entry.ErrorMessage = errMsg
	h.logAndAudit(entry, route.Name, "", "", 0, false)
	httputil.RespondErr(w, status, typ, msg)
}

// ── Misc ─────────────────────────────────────────────────────────────────────

func setQuotaHeaders(w http.ResponseWriter, limit, used, remaining int) {
	w.Header().Set(headerTokenLimit, strconv.Itoa(limit))
	w.Header().Set(headerTokenUsed, strconv.Itoa(used))
	w.Header().Set(headerTokenRem, strconv.Itoa(remaining))
}

func nonZeroOnly(m map[string]int) map[string]int {
	out := make(map[string]int)
	for k, v := range m {
		if v > 0 {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func mergePIITypes(a, b map[string]int) map[string]int {
	out := make(map[string]int, len(a)+len(b))
	for k, v := range a {
		out[k] += v
	}
	for k, v := range b {
		out[k] += v
	}
	return out
}

func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
