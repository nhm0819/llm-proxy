// Package middleware – API key authentication.
//
// The proxy issues its own API keys that map to user IDs.
// This prevents anonymous access and ties every request to a quota subject.
//
// Key format:  sk-proxy-<hex48>
// Storage:     Redis (primary) seeded from PROXY_API_KEYS_JSON on startup
//
// Per-request flow:
//  1. Extract "Bearer <key>" from Authorization header.
//  2. Resolve the key via the KeyResolver (Redis store).
//  3. Inject the resolved userID into X-User-ID header for downstream handlers.
//  4. Strip the proxy key from Authorization so it is never forwarded upstream.

package middleware

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/nhm0819/llm-proxy/pkg/httputil"
)

const headerUserID = "X-User-ID"

// KeyResolver resolves a proxy API key to a userID.
// Implementations: apikey.Store (Redis-backed), StaticKeyRegistry (env-var fallback).
type KeyResolver interface {
	Resolve(ctx context.Context, key string) (userID string, found bool, err error)
}

// StaticKeyRegistry is a simple in-memory map, used as a fallback when the
// Redis-backed store is unavailable, and for tests.
type StaticKeyRegistry map[string]string

// Resolve implements KeyResolver for StaticKeyRegistry.
func (r StaticKeyRegistry) Resolve(_ context.Context, key string) (string, bool, error) {
	userID, ok := r[key]
	return userID, ok, nil
}

// LoadStaticKeyRegistry reads PROXY_API_KEYS_JSON from the environment.
// Returns an empty registry (open access) if the variable is not set —
// safe only in development.
func LoadStaticKeyRegistry() StaticKeyRegistry {
	s := os.Getenv("PROXY_API_KEYS_JSON")
	if s == "" {
		log.Println("warn: PROXY_API_KEYS_JSON not set – proxy is unauthenticated")
		return StaticKeyRegistry{}
	}
	var reg StaticKeyRegistry
	if err := json.Unmarshal([]byte(s), &reg); err != nil {
		log.Fatalf("PROXY_API_KEYS_JSON parse error: %v", err)
	}
	log.Printf("loaded %d proxy API keys (static)", len(reg))
	return reg
}

// APIKeyRegistry is kept for backward compatibility with existing tests.
type APIKeyRegistry = StaticKeyRegistry

// LoadAPIKeyRegistry is an alias for LoadStaticKeyRegistry.
// Deprecated: use LoadStaticKeyRegistry.
func LoadAPIKeyRegistry() APIKeyRegistry { return LoadStaticKeyRegistry() }

// AuthAPIKey returns middleware that validates the Bearer token via resolver.
//
//   - If resolver is nil or a StaticKeyRegistry with no entries, the middleware
//     is a no-op (development mode).
//   - On success the resolved userID is written to X-User-ID and the
//     Authorization header is stripped so the upstream API key (set by the
//     router) is injected cleanly in proxy.go.
func AuthAPIKey(resolver KeyResolver) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// No-op when resolver is an empty static registry (dev mode).
			if reg, ok := resolver.(StaticKeyRegistry); ok && len(reg) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			key := extractBearer(r.Header.Get("Authorization"))
			if key == "" {
				httputil.RespondErr(w, http.StatusUnauthorized,
					"auth_error", "missing Authorization header")
				return
			}

			userID, found, err := resolver.Resolve(r.Context(), key)
			if err != nil {
				httputil.RespondErr(w, http.StatusBadGateway,
					"server_error", "auth backend error")
				return
			}
			if !found {
				httputil.RespondErr(w, http.StatusUnauthorized,
					"auth_error", "invalid api key")
				return
			}

			// Inject the verified user ID; strip the proxy key so the upstream
			// receives only the backend API key (set by the proxy handler).
			r = r.Clone(r.Context())
			r.Header.Set(headerUserID, userID)
			r.Header.Del("Authorization")

			next.ServeHTTP(w, r)
		})
	}
}

func extractBearer(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
