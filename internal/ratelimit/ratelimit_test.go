package ratelimit_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/nhm0819/llm-proxy/internal/ratelimit"
)

func newTestStore(t *testing.T) (*ratelimit.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return ratelimit.New(rdb), mr
}

func TestAllow_UnderLimit(t *testing.T) {
	store, _ := newTestStore(t)
	res, err := store.Allow(context.Background(), "user1", 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.OK {
		t.Error("first request should be allowed")
	}
	if res.Count != 1 {
		t.Errorf("expected count=1, got %d", res.Count)
	}
}

func TestAllow_AtLimit(t *testing.T) {
	store, mr := newTestStore(t)
	ctx := context.Background()
	limit := 3

	for i := 1; i <= limit; i++ {
		res, err := store.Allow(ctx, "user2", limit)
		if err != nil {
			t.Fatalf("unexpected error at request %d: %v", i, err)
		}
		if !res.OK {
			t.Errorf("request %d should be allowed (limit=%d)", i, limit)
		}
	}
	_ = mr // keep reference
}

func TestAllow_ExceedsLimit(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	limit := 2

	// exhaust the limit
	for range limit {
		_, _ = store.Allow(ctx, "user3", limit)
	}

	res, err := store.Allow(ctx, "user3", limit)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OK {
		t.Error("request over limit should be denied")
	}
	if res.Count != limit+1 {
		t.Errorf("expected count=%d, got %d", limit+1, res.Count)
	}
}

func TestAllow_DifferentUsers_Independent(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	// user-a exhausts their limit
	for range 3 {
		_, _ = store.Allow(ctx, "user-a", 3)
	}

	// user-b should still be fine
	res, err := store.Allow(ctx, "user-b", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Error("user-b should not be affected by user-a's limit")
	}
}

func TestAllow_TTLExpiry_Resets(t *testing.T) {
	store, mr := newTestStore(t)
	ctx := context.Background()

	// Exhaust limit
	for range 3 {
		_, _ = store.Allow(ctx, "user-ttl", 3)
	}

	// Verify blocked
	res, _ := store.Allow(ctx, "user-ttl", 3)
	if res.OK {
		t.Fatal("expected blocked after limit exhausted")
	}

	// Fast-forward time in miniredis (TTL is 2 seconds)
	mr.FastForward(3 * time.Second)

	// After TTL expiry the counter resets, new request should pass
	res, err := store.Allow(ctx, "user-ttl", 3)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Error("expected allowed after TTL expiry")
	}
	if res.Count != 1 {
		t.Errorf("expected count=1 after reset, got %d", res.Count)
	}
}

func TestAllow_LimitOfOne(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	res, _ := store.Allow(ctx, "user-one", 1)
	if !res.OK {
		t.Error("first request with limit=1 should pass")
	}

	res, _ = store.Allow(ctx, "user-one", 1)
	if res.OK {
		t.Error("second request with limit=1 should be blocked")
	}
}
