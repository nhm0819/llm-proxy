package quota_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/nhm0819/llm-proxy/internal/quota"
)

func newTestStore(t *testing.T) (*quota.Store, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return quota.New(rdb), mr
}

func TestReserve_AllowsWhenUnderLimit(t *testing.T) {
	store, _ := newTestStore(t)
	res, err := store.Reserve(context.Background(), "quota:user1:20240101", 1000, 300, 3600)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.OK {
		t.Error("expected reservation to succeed")
	}
	if res.Used != 300 {
		t.Errorf("expected Used=300, got %d", res.Used)
	}
	if res.Limit != 1000 {
		t.Errorf("expected Limit=1000, got %d", res.Limit)
	}
}

func TestReserve_BlocksWhenOverLimit(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	key := "quota:user2:20240101"

	// First reservation uses 800
	_, _ = store.Reserve(ctx, key, 1000, 800, 3600)

	// Second reservation wants 300 but only 200 remain
	res, err := store.Reserve(ctx, key, 1000, 300, 3600)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.OK {
		t.Error("expected reservation to be denied")
	}
}

func TestReserve_ExactLimit(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	key := "quota:user3:20240101"

	res, err := store.Reserve(ctx, key, 500, 500, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Error("exact-limit reservation should succeed")
	}

	// No more capacity
	res2, _ := store.Reserve(ctx, key, 500, 1, 3600)
	if res2.OK {
		t.Error("post-exact reservation should fail")
	}
}

func TestAdjust_PositiveDelta(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	key := "quota:user4:20240101"

	_, _ = store.Reserve(ctx, key, 1000, 500, 3600) // used=500
	newUsed, err := store.Adjust(ctx, key, 100, 3600) // used=600
	if err != nil {
		t.Fatal(err)
	}
	if newUsed != 600 {
		t.Errorf("expected 600, got %d", newUsed)
	}
}

func TestAdjust_NegativeDelta(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	key := "quota:user5:20240101"

	_, _ = store.Reserve(ctx, key, 1000, 500, 3600) // used=500
	newUsed, err := store.Adjust(ctx, key, -200, 3600) // used=300
	if err != nil {
		t.Fatal(err)
	}
	if newUsed != 300 {
		t.Errorf("expected 300, got %d", newUsed)
	}
}

func TestAdjust_ClampAtZero(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	key := "quota:user6:20240101"

	_, _ = store.Reserve(ctx, key, 1000, 100, 3600)
	// Refund more than was reserved
	newUsed, err := store.Adjust(ctx, key, -500, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if newUsed != 0 {
		t.Errorf("used should clamp at 0, got %d", newUsed)
	}
}

func TestDayKey_Format(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	ts := time.Date(2024, 3, 5, 9, 0, 0, 0, loc)
	key := quota.DayKey("alice", ts)
	if key != "quota:alice:20240305" {
		t.Errorf("unexpected DayKey: %q", key)
	}
}

func TestSecondsUntilMidnight(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Seoul")
	// 1 second before midnight
	ts := time.Date(2024, 1, 1, 23, 59, 59, 0, loc)
	s := quota.SecondsUntilMidnight(ts)
	if s != 1 {
		t.Errorf("expected 1 second until midnight, got %d", s)
	}

	// At midnight (edge case: should return 86400)
	ts2 := time.Date(2024, 1, 1, 0, 0, 0, 0, loc)
	s2 := quota.SecondsUntilMidnight(ts2)
	if s2 != 86400 {
		t.Errorf("expected 86400, got %d", s2)
	}
}

func TestReserve_Concurrent(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()
	key := "quota:concurrent:20240101"
	limit := 1000
	amount := 100

	// Run 10 goroutines each reserving 100 tokens
	errs := make(chan error, 10)
	for range 10 {
		go func() {
			_, err := store.Reserve(ctx, key, limit, amount, 3600)
			errs <- err
		}()
	}

	for range 10 {
		if err := <-errs; err != nil {
			t.Errorf("concurrent reserve error: %v", err)
		}
	}

	// Verify total: should be exactly 1000 (10 x 100)
	res, err := store.Reserve(ctx, key, limit, 1, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if res.OK {
		t.Error("expected no more capacity after 10 x 100 = 1000")
	}
	if res.Used != 1000 {
		t.Errorf("expected used=1000, got %d", res.Used)
	}
}

func TestReserve_MultipleKeys_Independent(t *testing.T) {
	store, _ := newTestStore(t)
	ctx := context.Background()

	// Exhaust key1
	_, _ = store.Reserve(ctx, "quota:a:20240101", 100, 100, 3600)

	// key2 should still have capacity
	res, err := store.Reserve(ctx, "quota:b:20240101", 100, 50, 3600)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK {
		t.Error("different key should have independent quota")
	}
}

func TestDayKey_SingleDigitMonth(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	ts := time.Date(2024, 1, 9, 0, 0, 0, 0, loc)
	key := quota.DayKey("user", ts)
	if key != "quota:user:20240109" {
		t.Errorf("expected zero-padded key, got %q", key)
	}
}

func TestSecondsUntilMidnight_ClampMin(t *testing.T) {
	loc, _ := time.LoadLocation("UTC")
	// Exactly at midnight boundary
	ts := time.Date(2024, 1, 1, 23, 59, 59, 999999999, loc)
	s := quota.SecondsUntilMidnight(ts)
	if s < 1 {
		t.Errorf("should clamp to at least 1 second, got %d", s)
	}
}
