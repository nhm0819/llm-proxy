package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nhm0819/llm-proxy/internal/metrics"
)

// newTestMetrics creates Metrics registered against a custom registry
// to avoid collisions with the default global registry across tests.
func newTestMetrics(t *testing.T) *metrics.Metrics {
	t.Helper()
	reg := prometheus.NewRegistry()
	m := &metrics.Metrics{
		RequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "test_requests_total",
		}, []string{"status", "kind", "upstream", "user_id"}),
		RequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "test_request_duration_seconds",
			Buckets: []float64{.05, .1, .5, 1},
		}, []string{"kind", "upstream"}),
		TokensUsed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "test_tokens_used_total",
		}, []string{"user_id", "kind", "model"}),
		QuotaRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "test_quota_rejected_total",
		}, []string{"user_id"}),
		RPSRejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "test_rps_rejected_total",
		}, []string{"user_id"}),
		PIIDetected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "test_pii_detected_total",
		}, []string{"kind", "pii_type", "direction"}),
		CircuitState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "test_circuit_state",
		}, []string{"upstream"}),
	}
	reg.MustRegister(
		m.RequestsTotal,
		m.RequestDuration,
		m.TokensUsed,
		m.QuotaRejected,
		m.RPSRejected,
		m.PIIDetected,
		m.CircuitState,
	)
	return m
}

func TestRecordRequest(t *testing.T) {
	m := newTestMetrics(t)
	// Should not panic
	m.RecordRequest(200, "chat", "openai", "alice", 0.5)
	m.RecordRequest(500, "chat", "openai", "alice", 1.0)
}

func TestRecordTokens(t *testing.T) {
	m := newTestMetrics(t)
	m.RecordTokens("alice", "chat", "gpt-4", 100)
	// Zero tokens should be a no-op
	m.RecordTokens("alice", "chat", "gpt-4", 0)
	// Negative tokens should also be a no-op
	m.RecordTokens("alice", "chat", "gpt-4", -5)
}

func TestRecordQuotaRejection(t *testing.T) {
	m := newTestMetrics(t)
	m.RecordQuotaRejection("bob")
}

func TestRecordRPSRejection(t *testing.T) {
	m := newTestMetrics(t)
	m.RecordRPSRejection("carol")
}

func TestRecordPII(t *testing.T) {
	m := newTestMetrics(t)
	m.RecordPII("chat", map[string]int{"email": 2, "phone": 1}, "request")
	// Empty map should be a no-op
	m.RecordPII("chat", map[string]int{}, "request")
	// Zero-count entries should be skipped
	m.RecordPII("chat", map[string]int{"ssn": 0}, "response")
}

func TestUpdateCircuitState(t *testing.T) {
	m := newTestMetrics(t)
	m.UpdateCircuitState("api.openai.com", 0) // closed
	m.UpdateCircuitState("api.openai.com", 1) // open
	m.UpdateCircuitState("api.openai.com", 2) // half-open
}

func TestHandler_NotNil(t *testing.T) {
	h := metrics.Handler()
	if h == nil {
		t.Fatal("expected non-nil handler")
	}
}
