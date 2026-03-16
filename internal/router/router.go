// Package router selects the upstream backend for a given model name.
// Rules are matched by longest prefix; the first rule with an empty prefix
// acts as the fallback default.
package router

import (
	"strings"

	"github.com/nhm0819/llm-proxy/internal/config"
)

// Router selects an upstream RouteRule for a model string.
type Router struct {
	rules        []config.RouteRule
	defaultRoute config.RouteRule
}

// New builds a Router from loaded configuration.
func New(cfg config.Config) *Router {
	return &Router{
		rules:        cfg.Routes,
		defaultRoute: cfg.DefaultRoute,
	}
}

// Pick returns the best-matching RouteRule for the given model name.
// Longest prefix wins; empty-prefix rules are skipped (they serve as default).
func (rt *Router) Pick(model string) config.RouteRule {
	best := rt.defaultRoute
	bestLen := -1
	for _, r := range rt.rules {
		if r.Prefix == "" {
			continue
		}
		if strings.HasPrefix(model, r.Prefix) && len(r.Prefix) > bestLen {
			best = r
			bestLen = len(r.Prefix)
		}
	}
	return best
}
