package httputil_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nhm0819/llm-proxy/pkg/httputil"
)

func TestCopyHeaders_CopiesAll(t *testing.T) {
	src := http.Header{}
	src.Set("Content-Type", "application/json")
	src.Add("X-Custom", "a")
	src.Add("X-Custom", "b")

	dst := http.Header{}
	dst.Set("Existing", "keep")

	httputil.CopyHeaders(dst, src)

	if dst.Get("Content-Type") != "application/json" {
		t.Errorf("expected Content-Type=application/json, got %q", dst.Get("Content-Type"))
	}
	if vals := dst.Values("X-Custom"); len(vals) != 2 {
		t.Errorf("expected 2 X-Custom values, got %d", len(vals))
	}
	if dst.Get("Existing") != "keep" {
		t.Error("existing header should be preserved")
	}
}

func TestCopyHeaders_EmptySrc(t *testing.T) {
	dst := http.Header{}
	dst.Set("Keep", "me")
	httputil.CopyHeaders(dst, http.Header{})
	if dst.Get("Keep") != "me" {
		t.Error("dst should be unchanged for empty src")
	}
}

func TestRespondErr_StatusAndBody(t *testing.T) {
	rec := httptest.NewRecorder()
	httputil.RespondErr(rec, http.StatusBadRequest, "invalid_request_error", "bad input")

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}

	var body map[string]map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response body: %v", err)
	}
	errObj, ok := body["error"]
	if !ok {
		t.Fatal("expected error key in response")
	}
	if errObj["type"] != "invalid_request_error" {
		t.Errorf("expected type=invalid_request_error, got %v", errObj["type"])
	}
	if errObj["message"] != "bad input" {
		t.Errorf("expected message=bad input, got %v", errObj["message"])
	}
}

func TestRespondErr_500(t *testing.T) {
	rec := httptest.NewRecorder()
	httputil.RespondErr(rec, http.StatusInternalServerError, "server_error", "oops")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("expected 500, got %d", rec.Code)
	}
}

func TestNewRequestID_NonEmpty(t *testing.T) {
	id := httputil.NewRequestID()
	if id == "" {
		t.Fatal("expected non-empty request ID")
	}
}

func TestNewRequestID_UniqueAcrossCalls(t *testing.T) {
	ids := make(map[string]struct{}, 100)
	for range 100 {
		id := httputil.NewRequestID()
		if _, dup := ids[id]; dup {
			t.Fatalf("duplicate ID: %s", id)
		}
		ids[id] = struct{}{}
	}
}

func TestNewRequestID_HexLength(t *testing.T) {
	id := httputil.NewRequestID()
	// 16 bytes = 32 hex chars
	if len(id) != 32 {
		t.Errorf("expected 32-char hex, got %d chars: %q", len(id), id)
	}
}
