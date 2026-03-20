package docs_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nhm0819/llm-proxy/internal/docs"
)

func TestRegister_SpecEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	docs.Register(mux)

	req := httptest.NewRequest("GET", "/docs/openapi.yaml", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for /docs/openapi.yaml, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "yaml") {
		t.Errorf("expected yaml content type, got %q", ct)
	}
	if rec.Body.Len() == 0 {
		t.Error("expected non-empty spec body")
	}
}

func TestRegister_UIEndpoint(t *testing.T) {
	mux := http.NewServeMux()
	docs.Register(mux)

	req := httptest.NewRequest("GET", "/docs/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for /docs/, got %d", rec.Code)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "text/html") {
		t.Errorf("expected text/html content type, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "swagger-ui") {
		t.Error("expected swagger-ui content in HTML")
	}
}

func TestRegister_UIHasNoCacheHeader(t *testing.T) {
	mux := http.NewServeMux()
	docs.Register(mux)

	req := httptest.NewRequest("GET", "/docs/", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("expected Cache-Control=no-cache, got %q", cc)
	}
}
