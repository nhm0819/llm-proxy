// Package middleware provides reusable HTTP middleware for the proxy.
package middleware

import (
	"log"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/nhm0819/llm-proxy/pkg/httputil"
)

const headerRequestID = "X-Request-ID"

// Chain applies middleware in order (first middleware is outermost).
func Chain(h http.Handler, mw ...func(http.Handler) http.Handler) http.Handler {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	return h
}

// RequestID ensures every request has an X-Request-ID header.
// If the client sends one it is propagated; otherwise a new ID is generated.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(headerRequestID)
		if id == "" {
			id = httputil.NewRequestID()
			r.Header.Set(headerRequestID, id)
		}
		w.Header().Set(headerRequestID, id)
		next.ServeHTTP(w, r)
	})
}

// Recover catches panics, logs a stack trace, and returns HTTP 500.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("PANIC recovered: %v\n%s", rec, debug.Stack())
				httputil.RespondErr(w, http.StatusInternalServerError, "server_error", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// AccessLog logs method, path, status code, and latency for every request.
// It wraps the ResponseWriter to capture the status code.
func AccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		log.Printf("access method=%s path=%s status=%d duration_ms=%d",
			r.Method, r.URL.Path, rw.status, time.Since(start).Milliseconds())
	})
}

// MaxBody rejects requests whose body exceeds limit bytes.
func MaxBody(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				httputil.RespondErr(w, http.StatusRequestEntityTooLarge,
					"invalid_request_error", "request body too large")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- responseWriter wrapper ----------

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// Flush implements http.Flusher so that SSE streaming works correctly when
// AccessLog wraps the underlying ResponseWriter.
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
