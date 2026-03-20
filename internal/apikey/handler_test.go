package apikey_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nhm0819/llm-proxy/internal/apikey"
)

func setupHandlerTest(t *testing.T) (*http.ServeMux, string) {
	t.Helper()
	store := setupTestStore(t)
	adminKey := "test-admin-key"
	handler := apikey.NewHandler(store, adminKey)
	mux := http.NewServeMux()
	handler.Register(mux)
	return mux, adminKey
}

func TestHandler_Create(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	body := `{"user_id":"alice","description":"test key"}`
	req := httptest.NewRequest("POST", "/admin/keys", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var result apikey.Key
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if result.Key == "" {
		t.Error("expected non-empty key")
	}
	if result.UserID != "alice" {
		t.Errorf("expected user_id=alice, got %q", result.UserID)
	}
}

func TestHandler_Create_MissingUserID(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	body := `{"description":"no user"}`
	req := httptest.NewRequest("POST", "/admin/keys", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_Create_InvalidJSON(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	req := httptest.NewRequest("POST", "/admin/keys", bytes.NewBufferString("not json"))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_Create_WithExpiry(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	body := `{"user_id":"bob","expires_at":"2099-12-31T00:00:00Z"}`
	req := httptest.NewRequest("POST", "/admin/keys", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var result apikey.Key
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if result.ExpiresAt == nil {
		t.Error("expected expires_at to be set")
	}
}

func TestHandler_Create_BadExpiry(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	body := `{"user_id":"bob","expires_at":"not-a-date"}`
	req := httptest.NewRequest("POST", "/admin/keys", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", rec.Code)
	}
}

func TestHandler_List(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	// Create two keys first
	for _, uid := range []string{"alice", "bob"} {
		body := `{"user_id":"` + uid + `"}`
		req := httptest.NewRequest("POST", "/admin/keys", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+adminKey)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
	}

	req := httptest.NewRequest("GET", "/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	count := int(result["count"].(float64))
	if count != 2 {
		t.Errorf("expected count=2, got %d", count)
	}
}

func TestHandler_Get(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	// Create a key
	body := `{"user_id":"carol"}`
	req := httptest.NewRequest("POST", "/admin/keys", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var created apikey.Key
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// Get that key
	req = httptest.NewRequest("GET", "/admin/keys/"+created.Key, nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var result apikey.Key
	_ = json.Unmarshal(rec.Body.Bytes(), &result)
	if result.UserID != "carol" {
		t.Errorf("expected user_id=carol, got %q", result.UserID)
	}
}

func TestHandler_Get_NotFound(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	req := httptest.NewRequest("GET", "/admin/keys/sk-proxy-nonexistent", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandler_Delete(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	// Create
	body := `{"user_id":"dave"}`
	req := httptest.NewRequest("POST", "/admin/keys", bytes.NewBufferString(body))
	req.Header.Set("Authorization", "Bearer "+adminKey)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	var created apikey.Key
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// Delete
	req = httptest.NewRequest("DELETE", "/admin/keys/"+created.Key, nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Errorf("expected 204, got %d", rec.Code)
	}

	// Verify deleted
	req = httptest.NewRequest("GET", "/admin/keys/"+created.Key, nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404 after delete, got %d", rec.Code)
	}
}

func TestHandler_Delete_NotFound(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	req := httptest.NewRequest("DELETE", "/admin/keys/sk-proxy-ghost", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	rec := httptest.NewRecorder()

	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("expected 404, got %d", rec.Code)
	}
}

func TestHandler_Auth_MissingToken(t *testing.T) {
	mux, _ := setupHandlerTest(t)

	req := httptest.NewRequest("GET", "/admin/keys", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandler_Auth_InvalidToken(t *testing.T) {
	mux, _ := setupHandlerTest(t)

	req := httptest.NewRequest("GET", "/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer wrong-admin-key")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
}

func TestHandler_Auth_Disabled(t *testing.T) {
	if !dockerAvailable() {
		t.Skip("skipping: Docker is not available")
	}
	store := setupTestStore(t)
	handler := apikey.NewHandler(store, "") // empty admin key = disabled
	mux := http.NewServeMux()
	handler.Register(mux)

	req := httptest.NewRequest("GET", "/admin/keys", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("expected 503 when admin disabled, got %d", rec.Code)
	}
}

func TestHandler_MethodNotAllowed(t *testing.T) {
	mux, adminKey := setupHandlerTest(t)

	req := httptest.NewRequest("PUT", "/admin/keys", nil)
	req.Header.Set("Authorization", "Bearer "+adminKey)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", rec.Code)
	}
}
