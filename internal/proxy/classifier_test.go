package proxy_test

import (
	"testing"

	"github.com/nhm0819/llm-proxy/internal/config"
	"github.com/nhm0819/llm-proxy/internal/proxy"
)

// ── ClassifyPath ──────────────────────────────────────────────────────────────

func TestClassifyPath(t *testing.T) {
	cases := []struct {
		path string
		want proxy.Kind
	}{
		{"/v1/chat/completions", proxy.KindChat},
		{"/v1/completions", proxy.KindCompletions},
		{"/v1/responses", proxy.KindResponses},
		{"/v1/embeddings", proxy.KindOther},
		{"/healthz", proxy.KindOther},
	}
	for _, c := range cases {
		got := proxy.ClassifyPath(c.path)
		if got != c.want {
			t.Errorf("ClassifyPath(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

// ── IsGenerative ─────────────────────────────────────────────────────────────

func TestIsGenerative(t *testing.T) {
	generative := []proxy.Kind{proxy.KindChat, proxy.KindCompletions, proxy.KindResponses}
	for _, k := range generative {
		if !proxy.IsGenerative(k) {
			t.Errorf("IsGenerative(%q) should be true", k)
		}
	}
	if proxy.IsGenerative(proxy.KindOther) {
		t.Error("IsGenerative(KindOther) should be false")
	}
}

// ── CollectRequestText ────────────────────────────────────────────────────────

func TestCollectRequestText_Chat(t *testing.T) {
	body := map[string]any{
		"model": "gpt-4",
		"messages": []any{
			map[string]any{"role": "user", "content": "hello world"},
			map[string]any{"role": "assistant", "content": "hi there"},
		},
	}
	text := proxy.CollectRequestText(proxy.KindChat, body)
	if text == "" {
		t.Fatal("expected non-empty text")
	}
	if !contains(text, "hello world") || !contains(text, "hi there") {
		t.Errorf("expected both message contents in text, got: %q", text)
	}
}

func TestCollectRequestText_ChatContentParts(t *testing.T) {
	body := map[string]any{
		"messages": []any{
			map[string]any{
				"role": "user",
				"content": []any{
					map[string]any{"type": "text", "text": "describe this image"},
				},
			},
		},
	}
	text := proxy.CollectRequestText(proxy.KindChat, body)
	if !contains(text, "describe this image") {
		t.Errorf("content part text not extracted: %q", text)
	}
}

func TestCollectRequestText_Completions(t *testing.T) {
	body := map[string]any{"prompt": "once upon a time"}
	text := proxy.CollectRequestText(proxy.KindCompletions, body)
	if text != "once upon a time" {
		t.Errorf("expected prompt text, got %q", text)
	}
}

func TestCollectRequestText_CompletionsArray(t *testing.T) {
	body := map[string]any{"prompt": []any{"hello", "world"}}
	text := proxy.CollectRequestText(proxy.KindCompletions, body)
	if !contains(text, "hello") || !contains(text, "world") {
		t.Errorf("expected both prompts, got %q", text)
	}
}

func TestCollectRequestText_Responses(t *testing.T) {
	body := map[string]any{
		"instructions": "you are a helpful assistant",
		"input":        "what is 2+2?",
	}
	text := proxy.CollectRequestText(proxy.KindResponses, body)
	if !contains(text, "you are a helpful assistant") {
		t.Errorf("instructions not included: %q", text)
	}
	if !contains(text, "what is 2+2?") {
		t.Errorf("input not included: %q", text)
	}
}

func TestCollectRequestText_NilBody(t *testing.T) {
	for _, k := range []proxy.Kind{proxy.KindChat, proxy.KindCompletions, proxy.KindResponses} {
		if text := proxy.CollectRequestText(k, nil); text != "" {
			t.Errorf("nil body should return empty string for %q, got %q", k, text)
		}
	}
}

// ── ExtractMaxOutputTokens ────────────────────────────────────────────────────

func TestExtractMaxOutputTokens_ExplicitChat(t *testing.T) {
	cfg := config.Config{DefaultMaxCompletionTokensChat: 512}
	body := map[string]any{"max_completion_tokens": float64(2048)}
	got := proxy.ExtractMaxOutputTokens(proxy.KindChat, body, cfg)
	if got != 2048 {
		t.Errorf("expected 2048, got %d", got)
	}
}

func TestExtractMaxOutputTokens_DefaultChat(t *testing.T) {
	cfg := config.Config{DefaultMaxCompletionTokensChat: 512}
	body := map[string]any{"model": "gpt-4"}
	got := proxy.ExtractMaxOutputTokens(proxy.KindChat, body, cfg)
	if got != 512 {
		t.Errorf("expected default 512, got %d", got)
	}
}

func TestExtractMaxOutputTokens_Responses(t *testing.T) {
	cfg := config.Config{DefaultMaxOutputTokensResponses: 1024}
	body := map[string]any{"max_output_tokens": float64(4096)}
	got := proxy.ExtractMaxOutputTokens(proxy.KindResponses, body, cfg)
	if got != 4096 {
		t.Errorf("expected 4096, got %d", got)
	}
}

// ── ExtractTotalTokens ────────────────────────────────────────────────────────

func TestExtractTotalTokens(t *testing.T) {
	body := map[string]any{
		"usage": map[string]any{
			"prompt_tokens":     float64(100),
			"completion_tokens": float64(50),
			"total_tokens":      float64(150),
		},
	}
	if got := proxy.ExtractTotalTokens(body); got != 150 {
		t.Errorf("expected 150, got %d", got)
	}
}

func TestExtractTotalTokens_Missing(t *testing.T) {
	if got := proxy.ExtractTotalTokens(map[string]any{}); got != 0 {
		t.Errorf("expected 0 for missing usage, got %d", got)
	}
	if got := proxy.ExtractTotalTokens(nil); got != 0 {
		t.Errorf("expected 0 for nil body, got %d", got)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}
