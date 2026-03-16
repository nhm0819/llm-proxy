// Package apikey manages proxy API keys backed by Redis.
// Keys are stored as Redis Hashes (apikey:<key>) and indexed in a Redis Set
// (apikeys:index).  The store satisfies middleware.KeyResolver so it can be
// plugged directly into the auth middleware.
package apikey

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	indexKey  = "apikeys:index"
	keyPrefix = "apikey:"
)

// ErrNotFound is returned when a key does not exist (or has expired).
var ErrNotFound = errors.New("apikey: not found")

// Key is the full metadata for a proxy API key.
type Key struct {
	Key         string     `json:"key"`
	UserID      string     `json:"user_id"`
	Description string     `json:"description,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	ExpiresAt   *time.Time `json:"expires_at,omitempty"`
}

// Store manages proxy API keys in Redis.
type Store struct {
	rdb *redis.Client
}

// New creates a Store backed by rdb.
func New(rdb *redis.Client) *Store {
	return &Store{rdb: rdb}
}

// Create generates a new key for userID and persists it.
// expiresAt is optional; if set the Redis key TTL is aligned to that time.
func (s *Store) Create(ctx context.Context, userID, description string, expiresAt *time.Time) (Key, error) {
	key := generateKey()
	k := Key{
		Key:         key,
		UserID:      userID,
		Description: description,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   expiresAt,
	}

	fields := map[string]any{
		"user_id":     userID,
		"description": description,
		"created_at":  k.CreatedAt.Format(time.RFC3339),
	}
	if expiresAt != nil {
		fields["expires_at"] = expiresAt.UTC().Format(time.RFC3339)
	}

	pipe := s.rdb.Pipeline()
	pipe.HSet(ctx, keyPrefix+key, fields)
	if expiresAt != nil {
		pipe.ExpireAt(ctx, keyPrefix+key, *expiresAt)
	}
	pipe.SAdd(ctx, indexKey, key)
	if _, err := pipe.Exec(ctx); err != nil {
		return Key{}, err
	}
	return k, nil
}

// Get returns the metadata for key. Returns ErrNotFound if missing or expired.
func (s *Store) Get(ctx context.Context, key string) (Key, error) {
	vals, err := s.rdb.HGetAll(ctx, keyPrefix+key).Result()
	if err != nil {
		return Key{}, err
	}
	if len(vals) == 0 {
		return Key{}, ErrNotFound
	}
	return parseKey(key, vals)
}

// Delete removes a key from Redis and the index.
func (s *Store) Delete(ctx context.Context, key string) error {
	pipe := s.rdb.Pipeline()
	pipe.Del(ctx, keyPrefix+key)
	pipe.SRem(ctx, indexKey, key)
	_, err := pipe.Exec(ctx)
	return err
}

// List returns all non-expired keys.  Stale index entries (expired TTL) are
// pruned automatically.
func (s *Store) List(ctx context.Context) ([]Key, error) {
	members, err := s.rdb.SMembers(ctx, indexKey).Result()
	if err != nil {
		return nil, err
	}

	out := make([]Key, 0, len(members))
	for _, m := range members {
		k, err := s.Get(ctx, m)
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				// Key has expired or been deleted; clean up the index silently.
				_ = s.rdb.SRem(ctx, indexKey, m)
				continue
			}
			return nil, err
		}
		out = append(out, k)
	}
	return out, nil
}

// Resolve implements middleware.KeyResolver.
// Returns userID and true when key exists and has not expired.
func (s *Store) Resolve(ctx context.Context, key string) (string, bool, error) {
	k, err := s.Get(ctx, key)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return "", false, nil
		}
		return "", false, err
	}
	if k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt) {
		return "", false, nil
	}
	return k.UserID, true, nil
}

// SeedFromMap imports keys from a static map (e.g. PROXY_API_KEYS_JSON) into
// Redis, skipping any key that already exists.  This enables a one-time
// migration from the legacy env-var approach.
func (s *Store) SeedFromMap(ctx context.Context, m map[string]string) error {
	for key, userID := range m {
		exists, err := s.rdb.Exists(ctx, keyPrefix+key).Result()
		if err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		pipe := s.rdb.Pipeline()
		pipe.HSet(ctx, keyPrefix+key, map[string]any{
			"user_id":     userID,
			"description": "seeded from PROXY_API_KEYS_JSON",
			"created_at":  time.Now().UTC().Format(time.RFC3339),
		})
		pipe.SAdd(ctx, indexKey, key)
		if _, err := pipe.Exec(ctx); err != nil {
			return err
		}
	}
	return nil
}

// ---------- helpers ----------

// generateKey produces a random "sk-proxy-<48 hex chars>" key.
func generateKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "sk-proxy-" + hex.EncodeToString(b)
}

func parseKey(key string, vals map[string]string) (Key, error) {
	k := Key{
		Key:         key,
		UserID:      vals["user_id"],
		Description: vals["description"],
	}
	if s := vals["created_at"]; s != "" {
		t, _ := time.Parse(time.RFC3339, s)
		k.CreatedAt = t
	}
	if s := vals["expires_at"]; s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err == nil {
			k.ExpiresAt = &t
		}
	}
	return k, nil
}
