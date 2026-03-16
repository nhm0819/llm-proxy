// Package loki pushes structured log entries to a Grafana Loki instance
// via its HTTP push API.
package loki

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Entry is the structured log line sent to Loki.
type Entry struct {
	Time       string `json:"time"`
	RequestID  string `json:"request_id"`
	UserID     string `json:"user_id,omitempty"`
	Path       string `json:"path"`
	Method     string `json:"method"`
	Kind       string `json:"kind,omitempty"`
	Model      string `json:"model,omitempty"`
	Upstream   string `json:"upstream,omitempty"`
	Status     int    `json:"status"`
	DurationMs int64  `json:"duration_ms"`

	PIIFound bool           `json:"pii_found"`
	PIITypes map[string]int `json:"pii_types,omitempty"`

	RequestExcerpt  string `json:"request_excerpt,omitempty"`
	ResponseExcerpt string `json:"response_excerpt,omitempty"`
	RequestHash     string `json:"request_hash,omitempty"`
	ResponseHash    string `json:"response_hash,omitempty"`

	PromptTokensEst   int `json:"prompt_tokens_est,omitempty"`
	ReserveTokens     int `json:"reserve_tokens,omitempty"`
	ActualTotalTokens int `json:"actual_total_tokens,omitempty"`

	TokenLimit     int `json:"token_limit,omitempty"`
	TokenUsed      int `json:"token_used,omitempty"`
	TokenRemaining int `json:"token_remaining,omitempty"`

	RPSLimit int `json:"rps_limit,omitempty"`
	RPSCount int `json:"rps_count,omitempty"`

	ErrorMessage string `json:"error,omitempty"`
}

// Pusher ships log entries to Loki asynchronously.
type Pusher struct {
	url        string
	baseLabels map[string]string
	client     *http.Client
}

// New returns a Pusher.  If url is empty, Push becomes a no-op.
func New(url string, baseLabels map[string]string) *Pusher {
	if baseLabels == nil {
		baseLabels = map[string]string{}
	}
	return &Pusher{
		url:        url,
		baseLabels: baseLabels,
		client:     &http.Client{Timeout: 3 * time.Second},
	}
}

// Push serialises entry as a JSON log line and sends it to Loki.
// streamLabels are merged with base labels for the Loki stream selector.
func (p *Pusher) Push(ctx context.Context, entry Entry, streamLabels map[string]string) error {
	if p.url == "" {
		return nil
	}

	line, err := json.Marshal(entry)
	if err != nil {
		return err
	}

	labels := make(map[string]string, len(p.baseLabels)+len(streamLabels))
	for k, v := range p.baseLabels {
		labels[k] = v
	}
	for k, v := range streamLabels {
		labels[k] = sanitize(v)
	}

	payload := map[string]any{
		"streams": []any{
			map[string]any{
				"stream": labels,
				"values": [][]string{
					{fmt.Sprintf("%d", time.Now().UnixNano()), string(line)},
				},
			},
		},
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("loki: push failed with status %s", resp.Status)
	}
	return nil
}

func sanitize(s string) string {
	return strings.TrimSpace(s)
}
