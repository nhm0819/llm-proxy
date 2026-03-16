// Package metrics exposes Prometheus metrics for the proxy.
//
// Registered metrics:
//
//	llm_proxy_requests_total            counter  – requests by status/kind/upstream/user
//	llm_proxy_request_duration_seconds  histogram – latency by kind/upstream
//	llm_proxy_tokens_used_total         counter  – actual tokens consumed by user/kind
//	llm_proxy_quota_rejected_total      counter  – daily quota rejections by user
//	llm_proxy_rps_rejected_total        counter  – RPS rejections by user
//	llm_proxy_pii_detected_total        counter  – PII detections by kind/pii_type
//	llm_proxy_circuit_state             gauge    – circuit breaker state by upstream (0=closed,1=open,2=half-open)
package metrics

import (
	"net/http"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds all registered Prometheus collectors.
type Metrics struct {
	RequestsTotal    *prometheus.CounterVec
	RequestDuration  *prometheus.HistogramVec
	TokensUsed       *prometheus.CounterVec
	QuotaRejected    *prometheus.CounterVec
	RPSRejected      *prometheus.CounterVec
	PIIDetected      *prometheus.CounterVec
	CircuitState     *prometheus.GaugeVec
}

// New registers and returns all metrics using the default registerer.
func New() *Metrics {
	return &Metrics{
		RequestsTotal: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_proxy_requests_total",
			Help: "Total number of proxy requests.",
		}, []string{"status", "kind", "upstream", "user_id"}),

		RequestDuration: promauto.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "llm_proxy_request_duration_seconds",
			Help:    "Proxy request latency in seconds.",
			Buckets: []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60, 120},
		}, []string{"kind", "upstream"}),

		TokensUsed: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_proxy_tokens_used_total",
			Help: "Actual total tokens consumed.",
		}, []string{"user_id", "kind", "model"}),

		QuotaRejected: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_proxy_quota_rejected_total",
			Help: "Requests rejected due to daily token quota.",
		}, []string{"user_id"}),

		RPSRejected: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_proxy_rps_rejected_total",
			Help: "Requests rejected due to RPS rate limit.",
		}, []string{"user_id"}),

		PIIDetected: promauto.NewCounterVec(prometheus.CounterOpts{
			Name: "llm_proxy_pii_detected_total",
			Help: "PII detections by type.",
		}, []string{"kind", "pii_type", "direction"}), // direction: request|response

		CircuitState: promauto.NewGaugeVec(prometheus.GaugeOpts{
			Name: "llm_proxy_circuit_state",
			Help: "Circuit breaker state per upstream (0=closed, 1=open, 2=half-open).",
		}, []string{"upstream"}),
	}
}

// Handler returns an http.Handler that serves the /metrics endpoint.
func Handler() http.Handler {
	return promhttp.Handler()
}

// RecordRequest records a completed request.
func (m *Metrics) RecordRequest(status int, kind, upstream, userID string, durationSec float64) {
	statusStr := statusBucket(status)
	m.RequestsTotal.WithLabelValues(statusStr, kind, upstream, userID).Inc()
	m.RequestDuration.WithLabelValues(kind, upstream).Observe(durationSec)
}

// RecordTokens records actual token usage.
func (m *Metrics) RecordTokens(userID, kind, model string, tokens int) {
	if tokens > 0 {
		m.TokensUsed.WithLabelValues(userID, kind, model).Add(float64(tokens))
	}
}

// RecordQuotaRejection increments the quota rejection counter.
func (m *Metrics) RecordQuotaRejection(userID string) {
	m.QuotaRejected.WithLabelValues(userID).Inc()
}

// RecordRPSRejection increments the RPS rejection counter.
func (m *Metrics) RecordRPSRejection(userID string) {
	m.RPSRejected.WithLabelValues(userID).Inc()
}

// RecordPII increments the PII detection counter for each type found.
func (m *Metrics) RecordPII(kind string, piiTypes map[string]int, direction string) {
	for piiType, count := range piiTypes {
		if count > 0 {
			m.PIIDetected.WithLabelValues(kind, piiType, direction).Add(float64(count))
		}
	}
}

// UpdateCircuitState sets the circuit state gauge for an upstream.
func (m *Metrics) UpdateCircuitState(upstream string, state float64) {
	m.CircuitState.WithLabelValues(upstream).Set(state)
}

// statusBucket converts an HTTP status code to a label-friendly string.
func statusBucket(status int) string {
	switch {
	case status < 200:
		return "1xx"
	case status < 300:
		return "2xx"
	case status < 400:
		return "3xx"
	case status < 500:
		return "4xx"
	default:
		return "5xx"
	}
}
