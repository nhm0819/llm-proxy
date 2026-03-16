// Package middleware – API key authentication.
//
// The proxy issues its own API keys that map to user IDs.
// This prevents anonymous access and ties every request to a quota subject.
//
// Key format:  sk-proxy-<hex32>
// Storage:     environment variable PROXY_API_KEYS_JSON
//              e.g. {"sk-proxy-abc123": "alice", "sk-proxy-def456": "bob"}
//
// Per-request flow:
//   1. Extract "Bearer <key>" from Authorization header.
//   2. Look up key in the registry.
//   3. Inject the resolved userID into X-User-ID header for downstream handlers.
//   4. Strip the proxy key from Authorization so it is never forwarded upstream.

package middleware

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/nhm0819/llm-proxy/pkg/httputil"
)

const headerUserID = "X-User-ID"

// APIKeyRegistry maps proxy API keys to user IDs.
type APIKeyRegistry map[string]string

// LoadAPIKeyRegistry reads PROXY_API_KEYS_JSON from the environment.
// Returns an empty registry (open access) if the variable is not set, which is
// safe only in development.
func LoadAPIKeyRegistry() APIKeyRegistry {
	s := os.Getenv("PROXY_API_KEYS_JSON")
	if s == "" {
		log.Println("warn: PROXY_API_KEYS_JSON not set – proxy is unauthenticated")
		return APIKeyRegistry{}
	}
	var reg APIKeyRegistry
	if err := json.Unmarshal([]byte(s), &reg); err != nil {
		log.Fatalf("PROXY_API_KEYS_JSON parse error: %v", err)
	}
	log.Printf("loaded %d proxy API keys", len(reg))
	return reg
}

// AuthAPIKey returns middleware that validates the Bearer token in the
// Authorization header against reg.
//
//   - If reg is empty the middleware is a no-op (development mode).
//   - On success the resolved userID is written to X-User-ID and the
//     Authorization header is stripped so the upstream API key (injected by
//     the router) is set cleanly in proxy.go.
func AuthAPIKey(reg APIKeyRegistry) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// No-op when registry is empty (dev mode)
			if len(reg) == 0 {
				next.ServeHTTP(w, r)
				return
			}

			key := extractBearer(r.Header.Get("Authorization"))
			if key == "" {
				httputil.RespondErr(w, http.StatusUnauthorized,
					"auth_error", "missing Authorization header")
				return
			}

			userID, ok := reg[key]
			if !ok {
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
