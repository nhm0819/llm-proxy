// Package apikey manages proxy API keys backed by PostgreSQL (primary) with
// Redis as a read-through cache.  The store satisfies middleware.KeyResolver
// so it can be plugged directly into the auth middleware.
package apikey

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

const (
	cachePrefix = "cache:apikey:"
	defaultTTL  = 5 * time.Minute
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

// Store manages proxy API keys with PostgreSQL as the primary store and Redis
// as a TTL-based read-through cache.
type Store struct {
	pool     *pgxpool.Pool
	rdb      *redis.Client
	cacheTTL time.Duration
}

// New creates a Store backed by a PostgreSQL pool and Redis cache.
func New(pool *pgxpool.Pool, rdb *redis.Client) *Store {
	return &Store{
		pool:     pool,
		rdb:      rdb,
		cacheTTL: defaultTTL,
	}
}

// Create generates a new key for userID, inserts it into PostgreSQL, and
// populates the Redis cache.
func (s *Store) Create(ctx context.Context, userID, description string, expiresAt *time.Time) (Key, error) {
	k := Key{
		Key:         generateKey(),
		UserID:      userID,
		Description: description,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   expiresAt,
	}

	_, err := s.pool.Exec(ctx,
		`INSERT INTO api_keys (key, user_id, description, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		k.Key, k.UserID, k.Description, k.CreatedAt, k.ExpiresAt,
	)
	if err != nil {
		return Key{}, fmt.Errorf("apikey: insert: %w", err)
	}

	_ = s.cacheSet(ctx, k)
	return k, nil
}

// Get returns the metadata for key. Returns ErrNotFound if missing or expired.
func (s *Store) Get(ctx context.Context, key string) (Key, error) {
	// Try cache first.
	if k, err := s.cacheGet(ctx, key); err == nil {
		return k, nil
	}

	// Cache miss — query PostgreSQL.
	k, err := s.pgGet(ctx, key)
	if err != nil {
		return Key{}, err
	}

	_ = s.cacheSet(ctx, k)
	return k, nil
}

// Delete removes a key from both Redis cache and PostgreSQL.
func (s *Store) Delete(ctx context.Context, key string) error {
	// Invalidate cache first to avoid stale reads.
	s.rdb.Del(ctx, cachePrefix+key)

	tag, err := s.pool.Exec(ctx, `DELETE FROM api_keys WHERE key = $1`, key)
	if err != nil {
		return fmt.Errorf("apikey: delete: %w", err)
	}
	_ = tag // we don't error on not-found for delete
	return nil
}

// List returns all non-expired keys directly from PostgreSQL (no cache).
func (s *Store) List(ctx context.Context) ([]Key, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT key, user_id, description, created_at, expires_at
		 FROM api_keys
		 WHERE expires_at IS NULL OR expires_at > now()
		 ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("apikey: list: %w", err)
	}
	defer rows.Close()

	var out []Key
	for rows.Next() {
		var k Key
		if err := rows.Scan(&k.Key, &k.UserID, &k.Description, &k.CreatedAt, &k.ExpiresAt); err != nil {
			return nil, fmt.Errorf("apikey: scan: %w", err)
		}
		out = append(out, k)
	}
	return out, rows.Err()
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
// PostgreSQL, skipping any key that already exists. This enables a one-time
// migration from the legacy env-var approach.
func (s *Store) SeedFromMap(ctx context.Context, m map[string]string) error {
	for key, userID := range m {
		_, err := s.pool.Exec(ctx,
			`INSERT INTO api_keys (key, user_id, description)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (key) DO NOTHING`,
			key, userID, "seeded from PROXY_API_KEYS_JSON",
		)
		if err != nil {
			return fmt.Errorf("apikey: seed %q: %w", key, err)
		}
	}
	return nil
}

// ---------- PostgreSQL helpers ----------

func (s *Store) pgGet(ctx context.Context, key string) (Key, error) {
	var k Key
	err := s.pool.QueryRow(ctx,
		`SELECT key, user_id, description, created_at, expires_at
		 FROM api_keys WHERE key = $1`, key,
	).Scan(&k.Key, &k.UserID, &k.Description, &k.CreatedAt, &k.ExpiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Key{}, ErrNotFound
		}
		return Key{}, fmt.Errorf("apikey: select: %w", err)
	}
	return k, nil
}

// ---------- Redis cache helpers ----------

func (s *Store) cacheGet(ctx context.Context, key string) (Key, error) {
	data, err := s.rdb.Get(ctx, cachePrefix+key).Bytes()
	if err != nil {
		return Key{}, err
	}
	var k Key
	if err := json.Unmarshal(data, &k); err != nil {
		return Key{}, err
	}
	return k, nil
}

func (s *Store) cacheSet(ctx context.Context, k Key) error {
	data, err := json.Marshal(k)
	if err != nil {
		return err
	}
	return s.rdb.Set(ctx, cachePrefix+k.Key, data, s.cacheTTL).Err()
}

// ---------- key generation ----------

// generateKey produces a random "sk-proxy-<48 hex chars>" key.
func generateKey() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return "sk-proxy-" + hex.EncodeToString(b)
}
