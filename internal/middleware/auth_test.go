package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nhm0819/llm-proxy/internal/middleware"
)

func TestAuthAPIKey_ValidKey(t *testing.T) {
	reg := middleware.APIKeyRegistry{"sk-proxy-abc": "alice"}
	handler := middleware.AuthAPIKey(reg)(echoHandler())

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-proxy-abc")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAuthAPIKey_MissingHeader(t *testing.T) {
	reg := middleware.APIKeyRegistry{"sk-proxy-abc": "alice"}
	handler := middleware.AuthAPIKey(reg)(echoHandler())

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthAPIKey_InvalidKey(t *testing.T) {
	reg := middleware.APIKeyRegistry{"sk-proxy-abc": "alice"}
	handler := middleware.AuthAPIKey(reg)(echoHandler())

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer wrong-key")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestAuthAPIKey_EmptyRegistry_NoOp(t *testing.T) {
	// Empty registry = dev mode, all requests pass
	handler := middleware.AuthAPIKey(middleware.APIKeyRegistry{})(echoHandler())

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("empty registry should pass all requests, got %d", rec.Code)
	}
}

func TestAuthAPIKey_InjectsUserID(t *testing.T) {
	reg := middleware.APIKeyRegistry{"sk-proxy-abc": "alice"}
	var capturedUserID string

	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedUserID = r.Header.Get("X-User-ID")
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.AuthAPIKey(reg)(capture)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-proxy-abc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedUserID != "alice" {
		t.Errorf("expected X-User-ID=alice, got %q", capturedUserID)
	}
}

func TestAuthAPIKey_StripsAuthorizationHeader(t *testing.T) {
	reg := middleware.APIKeyRegistry{"sk-proxy-abc": "alice"}
	var capturedAuth string

	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.AuthAPIKey(reg)(capture)

	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Authorization", "Bearer sk-proxy-abc")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if capturedAuth != "" {
		t.Errorf("Authorization header should be stripped, got %q", capturedAuth)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func echoHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}
