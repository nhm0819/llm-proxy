package proxy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/nhm0819/llm-proxy/internal/audit"
	"github.com/nhm0819/llm-proxy/internal/config"
	"github.com/nhm0819/llm-proxy/internal/loki"
	"github.com/nhm0819/llm-proxy/internal/pii"
	"github.com/nhm0819/llm-proxy/internal/proxy"
	"github.com/nhm0819/llm-proxy/internal/quota"
	"github.com/nhm0819/llm-proxy/internal/ratelimit"
	"github.com/nhm0819/llm-proxy/internal/router"
	"github.com/nhm0819/llm-proxy/internal/tokencount"
)

// ── Fixtures ─────────────────────────────────────────────────────────────────

// upstreamServer starts a fake upstream LLM API and returns it.
// respondFn is called for each request and returns the status + body.
func upstreamServer(t *testing.T, respondFn func(r *http.Request) (int, any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status, body := respondFn(r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if body != nil {
			_ = json.NewEncoder(w).Encode(body)
		}
	}))
}

// testProxy builds a Handler wired to the given upstream URL and a miniredis.
func testProxy(t *testing.T, upstream *httptest.Server, cfg config.Config) *proxy.Handler {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	cfg.Routes = []config.RouteRule{{
		Prefix:  "",
		Name:    "test-upstream",
		BaseURL: upstream.URL,
		APIKey:  "test-api-key",
	}}
	cfg.DefaultRoute = cfg.Routes[0]

	deps := proxy.Dependencies{
		Router:     router.New(cfg),
		PIIScanner: pii.New(),
		TokenCount: tokencount.New(),
		QuotaStore: quota.New(rdb),
		RLStore:    ratelimit.New(rdb),
		AuditStore: audit.New(rdb, audit.Config{
			StreamKey:    "audit:test",
			RecordTTLSec: 3600,
			StoreRecord:  false,
		}),
		LokiPusher: loki.New("", nil), // disabled
		HTTPClient: upstream.Client(),
	}
	return proxy.New(cfg, deps)
}

func defaultCfg() config.Config {
	return config.Config{
		MaxBodyBytes:                    config.DefaultMaxBodyBytes,
		DailyTokenLimit:                 100_000,
		TokenSafetyFactor:               1.2,
		DefaultMaxCompletionTokensChat:  512,
		DefaultMaxOutputTokensResponses: 512,
		QuotaTimezone:                   "UTC",
		RateLimitRPS:                    100,
		AuditStreamKey:                  "audit:test",
		AuditRecordTTLSeconds:           3600,
		UserDailyTokenLimitOverride:     map[string]int{},
		UserRateLimitOverrideRPS:        map[string]int{},
	}
}

func chatBody(model string) []byte {
	b, _ := json.Marshal(map[string]any{
		"model": model,
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	})
	return b
}

func chatResponse(content string) map[string]any {
	return map[string]any{
		"id":    "chatcmpl-test",
		"model": "gpt-4",
		"choices": []any{
			map[string]any{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": content,
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 5,
			"total_tokens":      15,
		},
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

func TestProxy_SuccessfulChatCompletion(t *testing.T) {
	upstream := upstreamServer(t, func(_ *http.Request) (int, any) {
		return 200, chatResponse("Hello there!")
	})
	defer upstream.Close()

	h := testProxy(t, upstream, defaultCfg())

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader(chatBody("gpt-4")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "user-alice")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d\nbody: %s", rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if _, ok := resp["choices"]; !ok {
		t.Error("response missing 'choices' field")
	}

	// Token headers should be present
	if rec.Header().Get("X-Token-Limit") == "" {
		t.Error("expected X-Token-Limit header")
	}
	if rec.Header().Get("X-Request-ID") == "" {
		t.Error("expected X-Request-ID header")
	}
}

func TestProxy_UpstreamError_Returns502(t *testing.T) {
	upstream := upstreamServer(t, func(_ *http.Request) (int, any) {
		return 500, map[string]any{"error": map[string]any{"message": "internal server error"}}
	})
	defer upstream.Close()

	h := testProxy(t, upstream, defaultCfg())

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader(chatBody("gpt-4")))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	h.ServeHTTP(rec, req)

	// Upstream 500 is passed through (not wrapped)
	if rec.Code != 500 {
		t.Errorf("expected 500 passthrough, got %d", rec.Code)
	}
}

func TestProxy_RPSRateLimit(t *testing.T) {
	upstream := upstreamServer(t, func(_ *http.Request) (int, any) {
		return 200, chatResponse("ok")
	})
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.RateLimitRPS = 2

	h := testProxy(t, upstream, cfg)

	// First 2 requests should succeed
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/v1/chat/completions",
			bytes.NewReader(chatBody("gpt-4")))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-User-ID", "rps-test-user")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Errorf("request %d: expected 200, got %d", i+1, rec.Code)
		}
	}

	// 3rd request in the same second should be rate-limited
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader(chatBody("gpt-4")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "rps-test-user")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("3rd request should be rate-limited (429), got %d", rec.Code)
	}
	assertErrorType(t, rec.Body.Bytes(), "rate_limit_error")
}

func TestProxy_DailyTokenQuotaExceeded(t *testing.T) {
	upstream := upstreamServer(t, func(_ *http.Request) (int, any) {
		return 200, chatResponse("ok")
	})
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.DailyTokenLimit = 1 // effectively zero – any request will exceed it

	h := testProxy(t, upstream, cfg)

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader(chatBody("gpt-4")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "quota-test-user")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("expected 429 for quota exceeded, got %d", rec.Code)
	}
	assertErrorType(t, rec.Body.Bytes(), "rate_limit_error")
}

func TestProxy_PIIBlock(t *testing.T) {
	upstream := upstreamServer(t, func(_ *http.Request) (int, any) {
		return 200, chatResponse("ok")
	})
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.PIIBlock = true

	h := testProxy(t, upstream, cfg)

	// Embed a fake email in the request
	body, _ := json.Marshal(map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "user", "content": "my email is test@example.com"},
		},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400 for PII block, got %d", rec.Code)
	}
	assertErrorType(t, rec.Body.Bytes(), "invalid_request_error")
}

func TestProxy_PIIDetected_NotBlocked(t *testing.T) {
	upstream := upstreamServer(t, func(_ *http.Request) (int, any) {
		return 200, chatResponse("ok")
	})
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.PIIBlock = false // detect but do not block

	h := testProxy(t, upstream, cfg)

	body, _ := json.Marshal(map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "user", "content": "contact me at user@example.com"},
		},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// Should proceed to upstream and return 200
	if rec.Code != 200 {
		t.Errorf("PII detect-only mode should pass request through, got %d", rec.Code)
	}
}

func TestProxy_NonGenerativeEndpoint_NoQuota(t *testing.T) {
	upstream := upstreamServer(t, func(_ *http.Request) (int, any) {
		return 200, map[string]any{"object": "list", "data": []any{}}
	})
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.DailyTokenLimit = 1 // quota is essentially full

	h := testProxy(t, upstream, cfg)

	// /v1/models is classified as "other" – should bypass quota
	req := httptest.NewRequest("GET", "/v1/models", nil)
	req.Header.Set("X-User-ID", "quota-bypass-user")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("non-generative endpoint should bypass quota, got %d", rec.Code)
	}
}

func TestProxy_RequestIDPropagation(t *testing.T) {
	var receivedReqID string
	upstream := upstreamServer(t, func(r *http.Request) (int, any) {
		receivedReqID = r.Header.Get("X-Request-ID")
		return 200, chatResponse("ok")
	})
	defer upstream.Close()

	h := testProxy(t, upstream, defaultCfg())

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader(chatBody("gpt-4")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "test-req-id-123")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if receivedReqID != "test-req-id-123" {
		t.Errorf("X-Request-ID not propagated to upstream, got %q", receivedReqID)
	}
	if rec.Header().Get("X-Request-ID") != "test-req-id-123" {
		t.Error("X-Request-ID not reflected in response")
	}
}

func TestProxy_UpstreamKeyInjected(t *testing.T) {
	var receivedAuth string
	upstream := upstreamServer(t, func(r *http.Request) (int, any) {
		receivedAuth = r.Header.Get("Authorization")
		return 200, chatResponse("ok")
	})
	defer upstream.Close()

	h := testProxy(t, upstream, defaultCfg())

	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader(chatBody("gpt-4")))
	req.Header.Set("Content-Type", "application/json")
	// Client sends a different (proxy) key; proxy should replace it
	req.Header.Set("Authorization", "Bearer client-key")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if receivedAuth != "Bearer test-api-key" {
		t.Errorf("upstream should receive the route API key, got %q", receivedAuth)
	}
}

func TestProxy_StreamingResponse_Passthrough(t *testing.T) {
	// Build a minimal SSE stream
	sseBody := strings.Join([]string{
		`data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}`,
		``,
		`data: {"choices":[{"delta":{"content":" world"},"index":0}]}`,
		``,
		`data: [DONE]`,
		``,
		``,
	}, "\n")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		_, _ = io.WriteString(w, sseBody)
	}))
	defer upstream.Close()

	h := testProxy(t, upstream, defaultCfg())

	body, _ := json.Marshal(map[string]any{
		"model":  "gpt-4",
		"stream": true,
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "stream-user")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("streaming: expected 200, got %d", rec.Code)
	}
	respStr := rec.Body.String()
	if !strings.Contains(respStr, "Hello") {
		t.Errorf("streaming: response body missing content: %q", respStr)
	}
}

func TestProxy_HealthCheck_NotHandled(t *testing.T) {
	// /healthz is handled by the mux, not the proxy; proxy should 404 or ignore it
	upstream := upstreamServer(t, func(_ *http.Request) (int, any) {
		return 404, nil
	})
	defer upstream.Close()

	h := testProxy(t, upstream, defaultCfg())

	req := httptest.NewRequest("GET", "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	// Proxy forwards everything under /v1/; /healthz goes to upstream and returns 404
	if rec.Code != 404 {
		t.Errorf("expected 404 from upstream for /healthz forwarded, got %d", rec.Code)
	}
}

func TestProxy_PerUserQuotaOverride(t *testing.T) {
	upstream := upstreamServer(t, func(_ *http.Request) (int, any) {
		return 200, chatResponse("ok")
	})
	defer upstream.Close()

	cfg := defaultCfg()
	cfg.DailyTokenLimit = 1                             // global: essentially full
	cfg.UserDailyTokenLimitOverride = map[string]int{
		"vip-user": 100_000, // vip gets real quota
	}

	h := testProxy(t, upstream, cfg)

	// Regular user is blocked
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader(chatBody("gpt-4")))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-User-ID", "regular-user")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Errorf("regular-user: expected 429, got %d", rec.Code)
	}

	// VIP user passes
	req2 := httptest.NewRequest("POST", "/v1/chat/completions",
		bytes.NewReader(chatBody("gpt-4")))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("X-User-ID", "vip-user")
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != 200 {
		t.Errorf("vip-user: expected 200, got %d", rec2.Code)
	}
}

func TestProxy_UserIDFromBodyField(t *testing.T) {
	var capturedPath string
	upstream := upstreamServer(t, func(r *http.Request) (int, any) {
		capturedPath = r.URL.Path
		return 200, chatResponse("ok")
	})
	defer upstream.Close()

	h := testProxy(t, upstream, defaultCfg())

	body, _ := json.Marshal(map[string]any{
		"model": "gpt-4",
		"user":  "body-user-id",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello"},
		},
	})
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if rec.Code != 200 {
		t.Errorf("expected 200, got %d", rec.Code)
	}
	_ = capturedPath
}

// ── Concurrent requests test ──────────────────────────────────────────────────

func TestProxy_ConcurrentRequests_NoRace(t *testing.T) {
	upstream := upstreamServer(t, func(_ *http.Request) (int, any) {
		return 200, chatResponse("ok")
	})
	defer upstream.Close()

	h := testProxy(t, upstream, defaultCfg())

	const goroutines = 20
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func(id int) {
			req := httptest.NewRequest("POST", "/v1/chat/completions",
				bytes.NewReader(chatBody("gpt-4")))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-User-ID", fmt.Sprintf("concurrent-user-%d", id))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != 200 && rec.Code != 429 {
				errs <- fmt.Errorf("goroutine %d: unexpected status %d", id, rec.Code)
				return
			}
			errs <- nil
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func assertErrorType(t *testing.T, body []byte, expectedType string) {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("response is not JSON: %v\nbody: %s", err, body)
	}
	errObj, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("missing 'error' field in response: %s", body)
	}
	if errObj["type"] != expectedType {
		t.Errorf("expected error type %q, got %q", expectedType, errObj["type"])
	}
}

// shared context for background operations in tests
var _ = context.Background
