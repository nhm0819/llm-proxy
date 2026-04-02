// Package apikey manages proxy API keys backed by a SQL database (PostgreSQL
// or SQLite) with an optional Redis read-through cache. The store satisfies
// middleware.KeyResolver so it can be plugged directly into the auth middleware.
package apikey

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

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

// Store manages proxy API keys with a SQL database as the primary store and
// an optional Redis TTL-based read-through cache.
type Store struct {
	db       SQLDB
	rdb      *redis.Client // nil when cache is disabled (e.g. tests)
	dialect  string
	cacheTTL time.Duration
}

// New creates a Store backed by a SQL database and Redis cache.
func New(db SQLDB, rdb *redis.Client, dialect string) *Store {
	return &Store{
		db:       db,
		rdb:      rdb,
		dialect:  dialect,
		cacheTTL: defaultTTL,
	}
}

// ph returns the correct placeholder for the n-th parameter (1-based).
// PostgreSQL uses $1, $2, ... while SQLite uses ?.
func (s *Store) ph(n int) string {
	if s.dialect == DialectSQLite {
		return "?"
	}
	return fmt.Sprintf("$%d", n)
}

// nowExpr returns the SQL expression for "current time" for the List query.
// For SQLite the format must match the RFC3339-style strings stored by the
// application so that TEXT comparison produces correct chronological ordering.
func (s *Store) nowExpr() string {
	if s.dialect == DialectSQLite {
		return "strftime('%Y-%m-%dT%H:%M:%fZ', 'now')"
	}
	return "now()"
}

// Create generates a new key for userID, inserts it into the database, and
// populates the Redis cache.
func (s *Store) Create(ctx context.Context, userID, description string, expiresAt *time.Time) (Key, error) {
	k := Key{
		Key:         generateKey(),
		UserID:      userID,
		Description: description,
		CreatedAt:   time.Now().UTC(),
		ExpiresAt:   expiresAt,
	}

	var expiresStr *string
	if k.ExpiresAt != nil {
		v := k.ExpiresAt.UTC().Format(time.RFC3339Nano)
		expiresStr = &v
	}

	query := fmt.Sprintf(
		`INSERT INTO api_keys (key, user_id, description, created_at, expires_at)
		 VALUES (%s, %s, %s, %s, %s)`,
		s.ph(1), s.ph(2), s.ph(3), s.ph(4), s.ph(5),
	)

	_, err := s.db.ExecContext(ctx, query,
		k.Key, k.UserID, k.Description,
		k.CreatedAt.UTC().Format(time.RFC3339Nano), expiresStr,
	)
	if err != nil {
		return Key{}, fmt.Errorf("apikey: insert: %w", err)
	}

	_ = s.cacheSet(ctx, k)
	return k, nil
}

// Get returns the metadata for key. Returns ErrNotFound if missing or expired.
func (s *Store) Get(ctx context.Context, key string) (Key, error) {
	if k, err := s.cacheGet(ctx, key); err == nil {
		return k, nil
	}

	k, err := s.dbGet(ctx, key)
	if err != nil {
		return Key{}, err
	}

	_ = s.cacheSet(ctx, k)
	return k, nil
}

// Delete removes a key from both Redis cache and the database.
func (s *Store) Delete(ctx context.Context, key string) error {
	if s.rdb != nil {
		s.rdb.Del(ctx, cachePrefix+key)
	}

	query := fmt.Sprintf(`DELETE FROM api_keys WHERE key = %s`, s.ph(1))
	_, err := s.db.ExecContext(ctx, query, key)
	if err != nil {
		return fmt.Errorf("apikey: delete: %w", err)
	}
	return nil
}

// List returns all non-expired keys directly from the database (no cache).
func (s *Store) List(ctx context.Context) ([]Key, error) {
	query := fmt.Sprintf(
		`SELECT key, user_id, description, created_at, expires_at
		 FROM api_keys
		 WHERE expires_at IS NULL OR expires_at > %s
		 ORDER BY created_at DESC`, s.nowExpr(),
	)

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("apikey: list: %w", err)
	}
	defer rows.Close()

	var out []Key
	for rows.Next() {
		k, err := s.scanKey(rows)
		if err != nil {
			return nil, err
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
// the database, skipping any key that already exists.
func (s *Store) SeedFromMap(ctx context.Context, m map[string]string) error {
	for key, userID := range m {
		query := fmt.Sprintf(
			`INSERT INTO api_keys (key, user_id, description, created_at)
			 VALUES (%s, %s, %s, %s)
			 ON CONFLICT (key) DO NOTHING`,
			s.ph(1), s.ph(2), s.ph(3), s.ph(4),
		)

		_, err := s.db.ExecContext(ctx, query,
			key, userID, "seeded from PROXY_API_KEYS_JSON",
			time.Now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return fmt.Errorf("apikey: seed %q: %w", key, err)
		}
	}
	return nil
}

// ---------- database helpers ----------

// scanner is satisfied by both *sql.Row and *sql.Rows.
type scanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanKey(sc scanner) (Key, error) {
	var (
		k          Key
		createdStr string
		expiresStr sql.NullString
	)
	if err := sc.Scan(&k.Key, &k.UserID, &k.Description, &createdStr, &expiresStr); err != nil {
		return Key{}, fmt.Errorf("apikey: scan: %w", err)
	}

	var err error
	k.CreatedAt, err = parseTime(createdStr)
	if err != nil {
		return Key{}, fmt.Errorf("apikey: parse created_at: %w", err)
	}

	if expiresStr.Valid {
		t, err := parseTime(expiresStr.String)
		if err != nil {
			return Key{}, fmt.Errorf("apikey: parse expires_at: %w", err)
		}
		k.ExpiresAt = &t
	}
	return k, nil
}

// parseTime attempts several common time formats to handle both PostgreSQL
// TIMESTAMPTZ output and ISO-8601/RFC3339 strings stored by the application.
func parseTime(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02T15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999-07",
		"2006-01-02 15:04:05-07:00",
		"2006-01-02 15:04:05-07",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("cannot parse %q as time", s)
}

func (s *Store) dbGet(ctx context.Context, key string) (Key, error) {
	query := fmt.Sprintf(
		`SELECT key, user_id, description, created_at, expires_at
		 FROM api_keys WHERE key = %s`, s.ph(1),
	)
	row := s.db.QueryRowContext(ctx, query, key)

	k, err := s.scanKey(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Key{}, ErrNotFound
		}
		return Key{}, err
	}
	return k, nil
}

// ---------- Redis cache helpers ----------

func (s *Store) cacheGet(ctx context.Context, key string) (Key, error) {
	if s.rdb == nil {
		return Key{}, errors.New("no cache")
	}
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
	if s.rdb == nil {
		return nil
	}
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
