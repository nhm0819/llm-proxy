package apikey

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/nhm0819/llm-proxy/pkg/httputil"
)

// Handler serves the admin REST API for key management.
//
// Routes (all require admin Bearer token):
//
//	POST   /admin/keys            create a new key
//	GET    /admin/keys            list all keys
//	GET    /admin/keys/{key}      get key metadata
//	DELETE /admin/keys/{key}      revoke a key
type Handler struct {
	store    *Store
	adminKey string // empty = admin API disabled
}

// NewHandler returns a Handler.  If adminKey is empty the admin endpoints
// respond with 503 Service Unavailable.
func NewHandler(store *Store, adminKey string) *Handler {
	return &Handler{store: store, adminKey: adminKey}
}

// Register mounts the admin routes on mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("/admin/keys", h.auth(http.HandlerFunc(h.keysCollection)))
	mux.Handle("/admin/keys/", h.auth(http.HandlerFunc(h.keysItem)))
}

// ---------- route handlers ----------

func (h *Handler) keysCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.create(w, r)
	case http.MethodGet:
		h.list(w, r)
	default:
		httputil.RespondErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

func (h *Handler) keysItem(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimPrefix(r.URL.Path, "/admin/keys/")
	if key == "" {
		h.keysCollection(w, r)
		return
	}
	switch r.Method {
	case http.MethodGet:
		h.get(w, r, key)
	case http.MethodDelete:
		h.revoke(w, r, key)
	default:
		httputil.RespondErr(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	}
}

// ---------- CRUD ----------

type createRequest struct {
	UserID      string  `json:"user_id"`
	Description string  `json:"description"`
	ExpiresAt   *string `json:"expires_at"` // RFC3339, optional
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httputil.RespondErr(w, http.StatusBadRequest, "invalid_request_error", "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.UserID) == "" {
		httputil.RespondErr(w, http.StatusBadRequest, "invalid_request_error", "user_id is required")
		return
	}

	var expiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt != "" {
		t, err := time.Parse(time.RFC3339, *req.ExpiresAt)
		if err != nil {
			httputil.RespondErr(w, http.StatusBadRequest, "invalid_request_error", "expires_at must be RFC3339 (e.g. 2026-12-31T00:00:00Z)")
			return
		}
		expiresAt = &t
	}

	k, err := h.store.Create(r.Context(), req.UserID, req.Description, expiresAt)
	if err != nil {
		httputil.RespondErr(w, http.StatusInternalServerError, "server_error", "failed to create key")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(k)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	keys, err := h.store.List(r.Context())
	if err != nil {
		httputil.RespondErr(w, http.StatusInternalServerError, "server_error", "failed to list keys")
		return
	}
	if keys == nil {
		keys = []Key{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": keys, "count": len(keys)})
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request, key string) {
	k, err := h.store.Get(r.Context(), key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.RespondErr(w, http.StatusNotFound, "not_found", "key not found")
			return
		}
		httputil.RespondErr(w, http.StatusInternalServerError, "server_error", "failed to get key")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(k)
}

func (h *Handler) revoke(w http.ResponseWriter, r *http.Request, key string) {
	if _, err := h.store.Get(r.Context(), key); err != nil {
		if errors.Is(err, ErrNotFound) {
			httputil.RespondErr(w, http.StatusNotFound, "not_found", "key not found")
			return
		}
		httputil.RespondErr(w, http.StatusInternalServerError, "server_error", "failed to revoke key")
		return
	}
	if err := h.store.Delete(r.Context(), key); err != nil {
		httputil.RespondErr(w, http.StatusInternalServerError, "server_error", "failed to revoke key")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------- admin auth middleware ----------

// auth wraps next with admin Bearer token validation.
// If adminKey is empty the endpoint responds with 503 (admin API disabled).
func (h *Handler) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.adminKey == "" {
			httputil.RespondErr(w, http.StatusServiceUnavailable, "admin_disabled",
				"admin API is disabled: set ADMIN_API_KEY to enable")
			return
		}
		token := extractBearer(r.Header.Get("Authorization"))
		if token == "" {
			httputil.RespondErr(w, http.StatusUnauthorized, "auth_error", "missing Authorization header")
			return
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(h.adminKey)) != 1 {
			httputil.RespondErr(w, http.StatusUnauthorized, "auth_error", "invalid admin key")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func extractBearer(header string) string {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}
