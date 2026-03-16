package breaker_test

import (
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/nhm0819/llm-proxy/internal/breaker"
)

// mockTransport records calls and returns configured responses.
type mockTransport struct {
	calls   int
	statusFn func(call int) int // returns status code per call number
	errFn    func(call int) error
}

func (m *mockTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.calls++
	if m.errFn != nil {
		if err := m.errFn(m.calls); err != nil {
			return nil, err
		}
	}
	status := 200
	if m.statusFn != nil {
		status = m.statusFn(m.calls)
	}
	return &http.Response{
		StatusCode: status,
		Body:       http.NoBody,
		Header:     make(http.Header),
	}, nil
}

func newReq(t *testing.T) *http.Request {
	t.Helper()
	req, err := http.NewRequest("POST", "https://api.openai.com/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestBreaker_ClosedByDefault(t *testing.T) {
	mock := &mockTransport{}
	b := breaker.New(mock, breaker.BreakerConfig{MaxFailures: 3, OpenTimeout: time.Second})

	resp, err := b.RoundTrip(newReq(t))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}
}

func TestBreaker_OpensAfterMaxFailures(t *testing.T) {
	mock := &mockTransport{statusFn: func(_ int) int { return 500 }}
	b := breaker.New(mock, breaker.BreakerConfig{MaxFailures: 3, OpenTimeout: time.Hour})

	for i := 0; i < 3; i++ {
		_, _ = b.RoundTrip(newReq(t))
	}

	// Next call should be rejected
	_, err := b.RoundTrip(newReq(t))
	if !errors.Is(err, breaker.ErrCircuitOpen) {
		t.Errorf("expected ErrCircuitOpen, got %v", err)
	}
}

func TestBreaker_HalfOpenAfterTimeout(t *testing.T) {
	mock := &mockTransport{statusFn: func(call int) int {
		if call <= 3 {
			return 500 // trigger open
		}
		return 200 // probe succeeds
	}}
	b := breaker.New(mock, breaker.BreakerConfig{
		MaxFailures: 3,
		OpenTimeout: 10 * time.Millisecond,
	})

	for i := 0; i < 3; i++ {
		_, _ = b.RoundTrip(newReq(t))
	}

	// Circuit is now open
	_, err := b.RoundTrip(newReq(t))
	if !errors.Is(err, breaker.ErrCircuitOpen) {
		t.Fatalf("expected open circuit, got %v", err)
	}

	// Wait for timeout
	time.Sleep(20 * time.Millisecond)

	// Probe should succeed and close the circuit
	resp, err := b.RoundTrip(newReq(t))
	if err != nil {
		t.Fatalf("probe should succeed, got: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Errorf("expected 200, got %d", resp.StatusCode)
	}

	// Circuit should now be closed
	if state := b.State("api.openai.com"); state != breaker.StateClosed {
		t.Errorf("expected Closed after probe, got %s", state)
	}
}

func TestBreaker_NetworkError_Counts(t *testing.T) {
	mock := &mockTransport{errFn: func(_ int) error { return errors.New("connection refused") }}
	b := breaker.New(mock, breaker.BreakerConfig{MaxFailures: 2, OpenTimeout: time.Hour})

	for i := 0; i < 2; i++ {
		_, _ = b.RoundTrip(newReq(t))
	}

	_, err := b.RoundTrip(newReq(t))
	if !errors.Is(err, breaker.ErrCircuitOpen) {
		t.Errorf("network errors should count as failures, got %v", err)
	}
}

func TestBreaker_SuccessResetFailures(t *testing.T) {
	calls := 0
	mock := &mockTransport{statusFn: func(call int) int {
		calls = call
		if call == 1 || call == 2 {
			return 500
		}
		return 200
	}}
	b := breaker.New(mock, breaker.BreakerConfig{MaxFailures: 5, OpenTimeout: time.Hour})

	_, _ = b.RoundTrip(newReq(t)) // fail
	_, _ = b.RoundTrip(newReq(t)) // fail
	_, _ = b.RoundTrip(newReq(t)) // success – should reset counter

	// 4 more failures should NOT open the circuit (counter was reset)
	for i := 0; i < 4; i++ {
		mock.statusFn = func(_ int) int { return 500 }
		_, _ = b.RoundTrip(newReq(t))
	}

	if state := b.State("api.openai.com"); state == breaker.StateOpen {
		t.Error("circuit should not be open; success should have reset failure count")
	}
	_ = calls
}
