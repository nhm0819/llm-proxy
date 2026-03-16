// Package ratelimit provides a per-user fixed-window RPS limiter backed by Redis.
package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store performs RPS rate-limit checks via Redis.
type Store struct {
	rdb    *redis.Client
	luaRPS *redis.Script
}

// Result is the outcome of a rate-limit check.
type Result struct {
	OK    bool
	Count int // current request count in this 1-second window
}

// New creates a Store backed by the given Redis client.
func New(rdb *redis.Client) *Store {
	// Fixed-window 1-second counter.
	// KEYS[1] = per-user per-second key  ARGV[1] = limit  ARGV[2] = TTL
	luaRPS := redis.NewScript(`
local key   = KEYS[1]
local limit = tonumber(ARGV[1])
local ttl   = tonumber(ARGV[2])

local n = redis.call("INCR", key)
if n == 1 then
  redis.call("EXPIRE", key, ttl)
end
if n > limit then
  return {0, n}
end
return {1, n}
`)
	return &Store{rdb: rdb, luaRPS: luaRPS}
}

// Allow checks whether userID is within their per-second request limit.
// The window key expires after 2 seconds (window + safety margin).
func (s *Store) Allow(ctx context.Context, userID string, limitRPS int) (Result, error) {
	key := fmt.Sprintf("rl:%s:%d", userID, time.Now().Unix())
	vals, err := s.luaRPS.Run(ctx, s.rdb, []string{key}, limitRPS, 2).Result()
	if err != nil {
		return Result{}, err
	}
	arr, ok := vals.([]any)
	if !ok || len(arr) < 2 {
		return Result{}, errors.New("ratelimit: unexpected redis result")
	}
	return Result{
		OK:    toInt(arr[0]) == 1,
		Count: toInt(arr[1]),
	}, nil
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	default:
		_ = t
		return 0
	}
}
