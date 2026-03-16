// Package audit records tamper-evident request/response audit events to a
// Redis Stream and optionally stores per-request detail hashes in Redis hashes.
package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Record is the structured payload written to both the Redis Stream and the
// per-request hash entry.
type Record struct {
	RequestID  string
	UserID     string
	Kind       string
	Model      string
	Upstream   string
	Status     int
	ReqHash    string
	RespHash   string
	TotalTokens int
	PIIFound   bool
}

// Store writes audit records to Redis.
type Store struct {
	rdb           *redis.Client
	streamKey     string
	recordTTL     time.Duration
	storeRecord   bool
	hmacKey       []byte // nil → plain SHA-256
}

// Config bundles audit-specific settings.
type Config struct {
	StreamKey     string
	RecordTTLSec  int
	StoreRecord   bool
	HMACKey       string
}

// New creates an audit Store.
func New(rdb *redis.Client, cfg Config) *Store {
	var hmacKey []byte
	if cfg.HMACKey != "" {
		hmacKey = []byte(cfg.HMACKey)
	}
	return &Store{
		rdb:         rdb,
		streamKey:   cfg.StreamKey,
		recordTTL:   time.Duration(cfg.RecordTTLSec) * time.Second,
		storeRecord: cfg.StoreRecord,
		hmacKey:     hmacKey,
	}
}

// Write appends rec to the Redis Stream and, if configured, stores a hash
// entry keyed by request ID.  It is safe to call from a goroutine.
func (s *Store) Write(ctx context.Context, rec Record) error {
	fields := map[string]any{
		"ts":            time.Now().Format(time.RFC3339Nano),
		"request_id":    rec.RequestID,
		"user_id":       rec.UserID,
		"kind":          rec.Kind,
		"model":         rec.Model,
		"upstream":      rec.Upstream,
		"status":        strconv.Itoa(rec.Status),
		"request_hash":  rec.ReqHash,
		"response_hash": rec.RespHash,
		"total_tokens":  strconv.Itoa(rec.TotalTokens),
		"pii_found":     strconv.FormatBool(rec.PIIFound),
	}

	pipe := s.rdb.Pipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{Stream: s.streamKey, Values: fields})
	if s.storeRecord {
		key := "audit:req:" + rec.RequestID
		pipe.HSet(ctx, key, fields)
		pipe.Expire(ctx, key, s.recordTTL)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// ---------- Hash builder ----------

// HashBuilder accumulates content and produces a hex-encoded digest.
// If an HMAC key is provided it uses HMAC-SHA256; otherwise plain SHA-256.
type HashBuilder struct {
	w     io.Writer
	sum   func() []byte
	empty bool
}

// NewHashBuilder returns a HashBuilder configured with the optional HMAC key.
func NewHashBuilder(hmacKey string) *HashBuilder {
	if hmacKey != "" {
		m := hmac.New(sha256.New, []byte(hmacKey))
		return &HashBuilder{w: m, sum: func() []byte { return m.Sum(nil) }, empty: true}
	}
	h := sha256.New()
	return &HashBuilder{w: h, sum: func() []byte { return h.Sum(nil) }, empty: true}
}

// WriteString adds s to the digest input.
func (hb *HashBuilder) WriteString(s string) {
	if s == "" {
		return
	}
	hb.empty = false
	_, _ = io.WriteString(hb.w, s)
}

// SumHex returns the current digest as a lowercase hex string.
func (hb *HashBuilder) SumHex() string { return hex.EncodeToString(hb.sum()) }

// Empty reports whether any content has been written.
func (hb *HashBuilder) Empty() bool { return hb.empty }
