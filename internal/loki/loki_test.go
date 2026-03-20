package loki_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nhm0819/llm-proxy/internal/loki"
)

func TestPush_NoOp_WhenURLEmpty(t *testing.T) {
	p := loki.New("", nil)
	err := p.Push(context.Background(), loki.Entry{
		RequestID: "req-1",
		Status:    200,
	}, nil)
	if err != nil {
		t.Fatalf("expected nil error for empty URL, got: %v", err)
	}
}

func TestPush_SendsToServer(t *testing.T) {
	var received bool
	var body map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received = true
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %q", ct)
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("failed to decode body: %v", err)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := loki.New(srv.URL, map[string]string{"app": "llm-proxy"})
	err := p.Push(context.Background(), loki.Entry{
		RequestID: "req-123",
		Method:    "POST",
		Path:      "/v1/chat/completions",
		Status:    200,
	}, map[string]string{"user": "alice"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !received {
		t.Fatal("server did not receive request")
	}
	// Check structure
	streams, ok := body["streams"]
	if !ok {
		t.Fatal("expected streams key")
	}
	arr, ok := streams.([]any)
	if !ok || len(arr) == 0 {
		t.Fatal("expected non-empty streams array")
	}
}

func TestPush_ReturnsErrorOnServerFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := loki.New(srv.URL, nil)
	err := p.Push(context.Background(), loki.Entry{Status: 200}, nil)
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
}

func TestPush_MergesLabels(t *testing.T) {
	var body map[string]any

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p := loki.New(srv.URL, map[string]string{"app": "proxy", "env": "test"})
	_ = p.Push(context.Background(), loki.Entry{}, map[string]string{"user": "bob"})

	// Verify labels are merged
	streams := body["streams"].([]any)
	stream := streams[0].(map[string]any)
	labels := stream["stream"].(map[string]any)

	if labels["app"] != "proxy" {
		t.Errorf("expected app=proxy, got %v", labels["app"])
	}
	if labels["user"] != "bob" {
		t.Errorf("expected user=bob, got %v", labels["user"])
	}
}

func TestNew_NilBaseLabels(t *testing.T) {
	// Should not panic
	p := loki.New("http://localhost:3100", nil)
	if p == nil {
		t.Fatal("expected non-nil Pusher")
	}
}
