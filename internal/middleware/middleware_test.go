package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nhm0819/llm-proxy/internal/middleware"
)

func TestRequestID_GeneratesWhenMissing(t *testing.T) {
	handler := middleware.RequestID(echoHandler())
	req := httptest.NewRequest("GET", "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	id := rec.Header().Get("X-Request-ID")
	if id == "" {
		t.Error("expected X-Request-ID header to be set")
	}
}

func TestRequestID_PreservesExisting(t *testing.T) {
	handler := middleware.RequestID(echoHandler())
	req := httptest.NewRequest("GET", "/test", nil)
	req.Header.Set("X-Request-ID", "my-custom-id")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	id := rec.Header().Get("X-Request-ID")
	if id != "my-custom-id" {
		t.Errorf("expected preserved ID 'my-custom-id', got %q", id)
	}
}

func TestRecover_CatchesPanic(t *testing.T) {
	panicker := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		panic("test panic")
	})

	handler := middleware.Recover(panicker)
	req := httptest.NewRequest("GET", "/panic", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500 after panic, got %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Error("expected error message in response body")
	}
}

func TestRecover_NoPanic(t *testing.T) {
	handler := middleware.Recover(echoHandler())
	req := httptest.NewRequest("GET", "/ok", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 when no panic, got %d", rec.Code)
	}
}

func TestMaxBody_RejectsLargeBody(t *testing.T) {
	handler := middleware.MaxBody(10)(echoHandler())
	req := httptest.NewRequest("POST", "/test", strings.NewReader("this body is way too long for the limit"))
	req.ContentLength = 100
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d", rec.Code)
	}
}

func TestMaxBody_AllowsSmallBody(t *testing.T) {
	handler := middleware.MaxBody(1000)(echoHandler())
	req := httptest.NewRequest("POST", "/test", strings.NewReader("short"))
	req.ContentLength = 5
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestMaxBody_ZeroContentLength_Passes(t *testing.T) {
	handler := middleware.MaxBody(10)(echoHandler())
	req := httptest.NewRequest("GET", "/test", nil)
	req.ContentLength = 0
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200 for zero content length, got %d", rec.Code)
	}
}

func TestChain_AppliesInOrder(t *testing.T) {
	var order []string

	mw1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "mw1-before")
			next.ServeHTTP(w, r)
			order = append(order, "mw1-after")
		})
	}
	mw2 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			order = append(order, "mw2-before")
			next.ServeHTTP(w, r)
			order = append(order, "mw2-after")
		})
	}

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		order = append(order, "handler")
		w.WriteHeader(http.StatusOK)
	})

	handler := middleware.Chain(inner, mw1, mw2)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	expected := []string{"mw1-before", "mw2-before", "handler", "mw2-after", "mw1-after"}
	if len(order) != len(expected) {
		t.Fatalf("expected %d steps, got %d: %v", len(expected), len(order), order)
	}
	for i, s := range expected {
		if order[i] != s {
			t.Errorf("step %d: expected %q, got %q", i, s, order[i])
		}
	}
}

func TestChain_Empty(t *testing.T) {
	inner := echoHandler()
	handler := middleware.Chain(inner)
	req := httptest.NewRequest("GET", "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", rec.Code)
	}
}

func TestAccessLog_SetsStatus(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	handler := middleware.AccessLog(inner)
	req := httptest.NewRequest("GET", "/missing", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

// flusherRecorder implements http.Flusher for testing.
type flusherRecorder struct {
	*httptest.ResponseRecorder
	flushed bool
}

func (f *flusherRecorder) Flush() {
	f.flushed = true
}
