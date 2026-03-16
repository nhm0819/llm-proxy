package router_test

import (
	"testing"

	"github.com/nhm0819/llm-proxy/internal/config"
	"github.com/nhm0819/llm-proxy/internal/router"
)

func TestPick_ExactPrefix(t *testing.T) {
	rt := router.New(config.Config{
		Routes: []config.RouteRule{
			{Prefix: "gpt-4", Name: "openai-gpt4", BaseURL: "https://api.openai.com"},
			{Prefix: "claude", Name: "anthropic", BaseURL: "https://api.anthropic.com"},
			{Prefix: "", Name: "default", BaseURL: "https://default.example.com"},
		},
		DefaultRoute: config.RouteRule{Prefix: "", Name: "default", BaseURL: "https://default.example.com"},
	})

	r := rt.Pick("claude-3-opus")
	if r.Name != "anthropic" {
		t.Errorf("expected anthropic, got %q", r.Name)
	}

	r = rt.Pick("gpt-4o")
	if r.Name != "openai-gpt4" {
		t.Errorf("expected openai-gpt4, got %q", r.Name)
	}
}

func TestPick_LongestPrefixWins(t *testing.T) {
	rt := router.New(config.Config{
		Routes: []config.RouteRule{
			{Prefix: "gpt", Name: "gpt-general", BaseURL: "https://a.example.com"},
			{Prefix: "gpt-4-turbo", Name: "gpt4-turbo-specific", BaseURL: "https://b.example.com"},
			{Prefix: "", Name: "default", BaseURL: "https://default.example.com"},
		},
		DefaultRoute: config.RouteRule{Name: "default", BaseURL: "https://default.example.com"},
	})

	r := rt.Pick("gpt-4-turbo-preview")
	if r.Name != "gpt4-turbo-specific" {
		t.Errorf("longest prefix should win, got %q", r.Name)
	}
}

func TestPick_Fallback(t *testing.T) {
	rt := router.New(config.Config{
		Routes: []config.RouteRule{
			{Prefix: "gpt", Name: "gpt-route", BaseURL: "https://a.example.com"},
			{Prefix: "", Name: "default", BaseURL: "https://default.example.com"},
		},
		DefaultRoute: config.RouteRule{Name: "default", BaseURL: "https://default.example.com"},
	})

	r := rt.Pick("unknown-model-xyz")
	if r.Name != "default" {
		t.Errorf("unknown model should fall back to default, got %q", r.Name)
	}
}

func TestPick_EmptyModel(t *testing.T) {
	rt := router.New(config.Config{
		Routes:       []config.RouteRule{{Prefix: "", Name: "default", BaseURL: "https://x.com"}},
		DefaultRoute: config.RouteRule{Name: "default", BaseURL: "https://x.com"},
	})
	r := rt.Pick("")
	if r.Name != "default" {
		t.Errorf("empty model should return default, got %q", r.Name)
	}
}
