package audit_test

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/nhm0819/llm-proxy/internal/audit"
)

func newTestRedis(t *testing.T) (*miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return mr, rdb
}

func newTestStore(t *testing.T, storePer bool) (*audit.Store, *miniredis.Miniredis, *redis.Client) {
	t.Helper()
	mr, rdb := newTestRedis(t)
	store := audit.New(rdb, audit.Config{
		StreamKey:    "audit:test",
		RecordTTLSec: 3600,
		StoreRecord:  storePer,
	})
	return store, mr, rdb
}

func TestWrite_AppendsToStream(t *testing.T) {
	store, _, rdb := newTestStore(t, false)
	ctx := context.Background()

	rec := audit.Record{
		RequestID:   "req-001",
		UserID:      "alice",
		Kind:        "chat",
		Model:       "gpt-4",
		Upstream:    "openai",
		Status:      200,
		ReqHash:     "abc123",
		RespHash:    "def456",
		TotalTokens: 100,
		PIIFound:    false,
	}
	if err := store.Write(ctx, rec); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Verify the stream has one entry via redis client
	entries, err := rdb.XRange(ctx, "audit:test", "-", "+").Result()
	if err != nil {
		t.Fatalf("XRange failed: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 stream entry, got %d", len(entries))
	}
}

func TestWrite_StoresHashRecord(t *testing.T) {
	store, _, rdb := newTestStore(t, true)
	ctx := context.Background()

	rec := audit.Record{
		RequestID: "req-store-001",
		UserID:    "bob",
		Status:    200,
	}
	if err := store.Write(ctx, rec); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Hash record should exist
	fields, err := rdb.HGetAll(ctx, "audit:req:req-store-001").Result()
	if err != nil {
		t.Fatalf("HGetAll failed: %v", err)
	}
	if len(fields) == 0 {
		t.Error("expected hash record to be stored")
	}
	if fields["request_id"] != "req-store-001" {
		t.Errorf("expected request_id=req-store-001, got %q", fields["request_id"])
	}
}

func TestWrite_NoHashRecord_WhenDisabled(t *testing.T) {
	store, _, rdb := newTestStore(t, false) // storeRecord=false
	ctx := context.Background()

	rec := audit.Record{RequestID: "req-no-store"}
	_ = store.Write(ctx, rec)

	fields, err := rdb.HGetAll(ctx, "audit:req:req-no-store").Result()
	if err != nil {
		t.Fatalf("HGetAll failed: %v", err)
	}
	if len(fields) > 0 {
		t.Error("hash record should not be stored when disabled")
	}
}

// ── HashBuilder ───────────────────────────────────────────────────────────────

func TestHashBuilder_SHA256(t *testing.T) {
	hb := audit.NewHashBuilder("")
	hb.WriteString("hello")
	hb.WriteString(" world")
	hex1 := hb.SumHex()

	hb2 := audit.NewHashBuilder("")
	hb2.WriteString("hello world")
	hex2 := hb2.SumHex()

	if hex1 != hex2 {
		t.Errorf("same content should produce same hash: %q vs %q", hex1, hex2)
	}
}

func TestHashBuilder_HMAC(t *testing.T) {
	hb := audit.NewHashBuilder("secret-key")
	hb.WriteString("hello world")
	hmacHex := hb.SumHex()

	// Without HMAC, hash should differ
	hb2 := audit.NewHashBuilder("")
	hb2.WriteString("hello world")
	sha256Hex := hb2.SumHex()

	if hmacHex == sha256Hex {
		t.Error("HMAC and SHA256 should produce different hashes for the same input")
	}
}

func TestHashBuilder_Empty(t *testing.T) {
	hb := audit.NewHashBuilder("")
	if !hb.Empty() {
		t.Error("new builder should be empty")
	}
	hb.WriteString("x")
	if hb.Empty() {
		t.Error("builder should not be empty after WriteString")
	}
}

func TestHashBuilder_EmptyStringNoOp(t *testing.T) {
	hb := audit.NewHashBuilder("")
	hb.WriteString("") // empty string → no-op
	if !hb.Empty() {
		t.Error("writing empty string should not change empty state")
	}
}

func TestHashBuilder_Deterministic(t *testing.T) {
	const input = "test payload 123"
	hashes := make([]string, 5)
	for i := range hashes {
		hb := audit.NewHashBuilder("key")
		hb.WriteString(input)
		hashes[i] = hb.SumHex()
	}
	for i := 1; i < len(hashes); i++ {
		if hashes[i] != hashes[0] {
			t.Errorf("hash[%d]=%q differs from hash[0]=%q", i, hashes[i], hashes[0])
		}
	}
}

// ── TTL verification ──────────────────────────────────────────────────────────

func TestWrite_RecordTTLSet(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	store := audit.New(rdb, audit.Config{
		StreamKey:    "audit:test",
		RecordTTLSec: 60,
		StoreRecord:  true,
	})

	ctx := context.Background()
	_ = store.Write(ctx, audit.Record{RequestID: "ttl-test"})

	// mr.TTL returns time.Duration (single value, no error)
	ttl := mr.TTL("audit:req:ttl-test")
	// TTL should be ~60 seconds
	if ttl < 55*time.Second || ttl > 65*time.Second {
		t.Errorf("expected TTL ~60s, got %v", ttl)
	}
}
