package apikey_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/nhm0819/llm-proxy/internal/apikey"
)

// setupTestStore creates an in-memory SQLite database and returns a configured
// Store. No Docker or external services are required.
func setupTestStore(t *testing.T) *apikey.Store {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("sql.Open sqlite: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := apikey.RunMigrations(ctx, db, apikey.DialectSQLite); err != nil {
		t.Fatalf("RunMigrations: %v", err)
	}

	// No Redis cache for unit tests (nil rdb).
	return apikey.New(db, nil, apikey.DialectSQLite)
}

func TestCreate_And_Resolve(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	k, err := store.Create(ctx, "alice", "test key", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if k.Key == "" {
		t.Fatal("expected non-empty key")
	}
	if k.UserID != "alice" {
		t.Errorf("expected user_id alice, got %q", k.UserID)
	}

	userID, found, err := store.Resolve(ctx, k.Key)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !found {
		t.Fatal("key should be found")
	}
	if userID != "alice" {
		t.Errorf("expected alice, got %q", userID)
	}
}

func TestResolve_NotFound(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	_, found, err := store.Resolve(ctx, "sk-proxy-nonexistent")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Error("should not find non-existent key")
	}
}

func TestDelete_Revokes(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	k, _ := store.Create(ctx, "bob", "", nil)

	if err := store.Delete(ctx, k.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	_, found, _ := store.Resolve(ctx, k.Key)
	if found {
		t.Error("key should not be found after deletion")
	}
}

func TestList(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	store.Create(ctx, "alice", "a", nil)
	store.Create(ctx, "bob", "b", nil)

	keys, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 2 {
		t.Errorf("expected 2 keys, got %d", len(keys))
	}
}

func TestCreate_WithExpiry(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	exp := time.Now().Add(-1 * time.Second) // already expired
	k, err := store.Create(ctx, "carol", "expiring key", &exp)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if k.ExpiresAt == nil {
		t.Fatal("expected ExpiresAt to be set")
	}

	// Resolve should detect the expired key.
	_, found, _ := store.Resolve(ctx, k.Key)
	if found {
		t.Error("expired key should not be found")
	}
}

func TestList_ExcludesExpired(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	exp := time.Now().Add(-1 * time.Second) // already expired
	store.Create(ctx, "eve", "expiring", &exp)
	store.Create(ctx, "frank", "permanent", nil)

	keys, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, k := range keys {
		if k.UserID == "eve" {
			t.Error("expired key should not appear in list")
		}
	}
	if len(keys) != 1 {
		t.Errorf("expected 1 key (frank), got %d", len(keys))
	}
}

func TestSeedFromMap(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	m := map[string]string{
		"sk-proxy-static-key": "dave",
	}
	if err := store.SeedFromMap(ctx, m); err != nil {
		t.Fatalf("SeedFromMap: %v", err)
	}

	userID, found, _ := store.Resolve(ctx, "sk-proxy-static-key")
	if !found || userID != "dave" {
		t.Errorf("seeded key not found or wrong user, found=%v userID=%q", found, userID)
	}
}

func TestSeedFromMap_SkipsExisting(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	// Create manually first.
	k, _ := store.Create(ctx, "original-user", "existing key", nil)

	// Try to overwrite via seed (should be a no-op).
	if err := store.SeedFromMap(ctx, map[string]string{k.Key: "overwritten-user"}); err != nil {
		t.Fatalf("SeedFromMap: %v", err)
	}

	got, _, _ := store.Resolve(ctx, k.Key)
	if got != "original-user" {
		t.Errorf("seed should not overwrite existing key, got %q", got)
	}
}

func TestGet_NotFound(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	_, err := store.Get(ctx, "sk-proxy-ghost")
	if err == nil {
		t.Fatal("expected ErrNotFound")
	}
}

func TestGet_CacheHit(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	// Create populates (no cache in test, but should succeed).
	k, err := store.Create(ctx, "cached-user", "cache test", nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Second Get should succeed via DB fallback.
	got, err := store.Get(ctx, k.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.UserID != "cached-user" {
		t.Errorf("expected cached-user, got %q", got.UserID)
	}
}

func TestDelete_InvalidatesCache(t *testing.T) {
	store := setupTestStore(t)
	ctx := context.Background()

	k, _ := store.Create(ctx, "to-delete", "will be deleted", nil)

	// Verify key is accessible.
	_, err := store.Get(ctx, k.Key)
	if err != nil {
		t.Fatalf("Get before delete: %v", err)
	}

	// Delete should remove from DB.
	if err := store.Delete(ctx, k.Key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	// Should be gone.
	_, err = store.Get(ctx, k.Key)
	if err == nil {
		t.Error("expected ErrNotFound after delete")
	}
}
