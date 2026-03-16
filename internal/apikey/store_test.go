package apikey_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/nhm0819/llm-proxy/internal/apikey"
)

func newStore(t *testing.T) (*apikey.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return apikey.New(rdb), mr
}

func TestCreate_And_Resolve(t *testing.T) {
	store, _ := newStore(t)
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
	store, _ := newStore(t)
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
	store, _ := newStore(t)
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
	store, _ := newStore(t)
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
	store, mr := newStore(t)
	ctx := context.Background()

	exp := time.Now().Add(1 * time.Hour)
	k, err := store.Create(ctx, "carol", "expiring key", &exp)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if k.ExpiresAt == nil {
		t.Fatal("expected ExpiresAt to be set")
	}

	// Advance miniredis clock past expiry
	mr.FastForward(2 * time.Hour)

	_, found, _ := store.Resolve(ctx, k.Key)
	if found {
		t.Error("expired key should not be found")
	}
}

func TestSeedFromMap(t *testing.T) {
	store, _ := newStore(t)
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
	store, _ := newStore(t)
	ctx := context.Background()

	// Create manually first
	k, _ := store.Create(ctx, "original-user", "existing key", nil)

	// Try to overwrite via seed (should be a no-op)
	if err := store.SeedFromMap(ctx, map[string]string{k.Key: "overwritten-user"}); err != nil {
		t.Fatalf("SeedFromMap: %v", err)
	}

	got, _, _ := store.Resolve(ctx, k.Key)
	if got != "original-user" {
		t.Errorf("seed should not overwrite existing key, got %q", got)
	}
}

func TestGet_NotFound(t *testing.T) {
	store, _ := newStore(t)
	ctx := context.Background()

	_, err := store.Get(ctx, "sk-proxy-ghost")
	if err == nil {
		t.Fatal("expected ErrNotFound")
	}
}

func TestList_PrunesExpiredIndexEntries(t *testing.T) {
	store, mr := newStore(t)
	ctx := context.Background()

	exp := time.Now().Add(1 * time.Minute)
	store.Create(ctx, "eve", "expiring", &exp)
	store.Create(ctx, "frank", "permanent", nil)

	mr.FastForward(2 * time.Minute)

	keys, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	// Only "frank"'s permanent key remains
	for _, k := range keys {
		if k.UserID == "eve" {
			t.Error("expired key should not appear in list")
		}
	}
}
