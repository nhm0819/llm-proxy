// Package httputil provides small HTTP utilities shared across the proxy.
package httputil

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// CopyHeaders copies all headers from src to dst.
// Existing dst values are preserved (Add semantics).
func CopyHeaders(dst, src http.Header) {
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// RespondErr writes a JSON error body in the OpenAI-compatible error envelope.
func RespondErr(w http.ResponseWriter, status int, typ, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"type":    typ,
			"message": msg,
		},
	})
}

// NewRequestID generates a random 16-byte hex request ID.
// Falls back to a nanosecond timestamp if the random source is unavailable.
func NewRequestID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}
