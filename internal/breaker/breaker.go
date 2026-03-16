// Package breaker wraps an http.Client with a per-upstream circuit breaker.
//
// State machine (per upstream name):
//
//	Closed   → normal operation; failures increment a counter.
//	Open     → all calls fail fast; re-evaluates after OpenTimeout.
//	HalfOpen → one probe request is allowed; success → Closed, failure → Open.
//
// Configuration is via BreakerConfig; sensible defaults are provided.
package breaker

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"
)

// ErrCircuitOpen is returned when a call is rejected because the circuit is open.
var ErrCircuitOpen = errors.New("circuit breaker: circuit is open")

// State represents the circuit state.
type State int

const (
	StateClosed   State = iota // normal
	StateOpen                  // failing fast
	StateHalfOpen             // probing
)

func (s State) String() string {
	switch s {
	case StateClosed:
		return "closed"
	case StateOpen:
		return "open"
	case StateHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// BreakerConfig controls circuit breaker behaviour.
type BreakerConfig struct {
	// MaxFailures is the number of consecutive upstream failures before the
	// circuit opens.  Default: 5.
	MaxFailures int

	// OpenTimeout is how long the circuit stays open before entering half-open.
	// Default: 30s.
	OpenTimeout time.Duration

	// IsFailure classifies an HTTP response as a failure.
	// Default: status >= 500.
	IsFailure func(*http.Response) bool
}

func (c *BreakerConfig) applyDefaults() {
	if c.MaxFailures <= 0 {
		c.MaxFailures = 5
	}
	if c.OpenTimeout <= 0 {
		c.OpenTimeout = 30 * time.Second
	}
	if c.IsFailure == nil {
		c.IsFailure = func(r *http.Response) bool { return r != nil && r.StatusCode >= 500 }
	}
}

// circuit holds the state for one upstream.
type circuit struct {
	mu          sync.Mutex
	state       State
	failures    int
	openedAt    time.Time
	cfg         BreakerConfig
	upstreamName string
}

func (c *circuit) allow() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch c.state {
	case StateClosed:
		return nil
	case StateOpen:
		if time.Since(c.openedAt) >= c.cfg.OpenTimeout {
			log.Printf("breaker[%s]: half-open (probing)", c.upstreamName)
			c.state = StateHalfOpen
			return nil
		}
		return fmt.Errorf("%w (upstream=%s)", ErrCircuitOpen, c.upstreamName)
	case StateHalfOpen:
		return nil
	default:
		return nil
	}
}

func (c *circuit) recordSuccess() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.state == StateHalfOpen {
		log.Printf("breaker[%s]: closed (probe succeeded)", c.upstreamName)
	}
	c.state = StateClosed
	c.failures = 0
}

func (c *circuit) recordFailure() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failures++
	if c.state == StateHalfOpen || c.failures >= c.cfg.MaxFailures {
		if c.state != StateOpen {
			log.Printf("breaker[%s]: open (failures=%d)", c.upstreamName, c.failures)
		}
		c.state = StateOpen
		c.openedAt = time.Now()
		c.failures = 0
	}
}

// Breaker wraps an http.RoundTripper and applies per-upstream circuit breaking.
// The upstream key is taken from the outgoing request's host + path prefix
// (anything before /v1/).
type Breaker struct {
	next    http.RoundTripper
	cfg     BreakerConfig
	mu      sync.Mutex
	circuits map[string]*circuit
}

// New wraps next with circuit-breaking behaviour.
func New(next http.RoundTripper, cfg BreakerConfig) *Breaker {
	cfg.applyDefaults()
	return &Breaker{
		next:     next,
		cfg:      cfg,
		circuits: make(map[string]*circuit),
	}
}

// RoundTrip implements http.RoundTripper.
func (b *Breaker) RoundTrip(req *http.Request) (*http.Response, error) {
	key := req.URL.Host
	cb := b.getOrCreate(key)

	if err := cb.allow(); err != nil {
		return nil, err
	}

	resp, err := b.next.RoundTrip(req)
	if err != nil {
		cb.recordFailure()
		return nil, err
	}
	if b.cfg.IsFailure(resp) {
		cb.recordFailure()
	} else {
		cb.recordSuccess()
	}
	return resp, nil
}

func (b *Breaker) getOrCreate(key string) *circuit {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cb, ok := b.circuits[key]; ok {
		return cb
	}
	cb := &circuit{cfg: b.cfg, state: StateClosed, upstreamName: key}
	b.circuits[key] = cb
	return cb
}

// State returns the current circuit state for the given upstream host (for
// observability / metrics).
func (b *Breaker) State(upstreamHost string) State {
	b.mu.Lock()
	defer b.mu.Unlock()
	if cb, ok := b.circuits[upstreamHost]; ok {
		cb.mu.Lock()
		defer cb.mu.Unlock()
		return cb.state
	}
	return StateClosed
}
