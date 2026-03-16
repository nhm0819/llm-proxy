// Package quota manages per-user daily token quotas backed by Redis.
// It uses Lua scripts for atomic check-and-reserve and post-call adjustment,
// ensuring correctness under concurrent requests.
package quota

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// Store handles token quota operations.
type Store struct {
	rdb            *redis.Client
	luaReserve     *redis.Script
	luaAdjust      *redis.Script
}

// ReserveResult is the outcome of a quota reservation attempt.
type ReserveResult struct {
	OK    bool
	Used  int // tokens used *after* this reservation
	Limit int
}

// New creates a Store using the provided Redis client.
// It registers the Lua scripts against the server on first run (EVALSHA).
func New(rdb *redis.Client) *Store {
	// Atomically check remaining quota and increment if allowed.
	// Returns: {ok(0|1), newUsed, limit}
	luaReserve := redis.NewScript(`
local key    = KEYS[1]
local limit  = tonumber(ARGV[1])
local amount = tonumber(ARGV[2])
local ttl    = tonumber(ARGV[3])

local used = tonumber(redis.call("GET", key) or "0")
if used + amount > limit then
  return {0, used, limit}
end
local newUsed = redis.call("INCRBY", key, amount)
if newUsed == amount then
  redis.call("EXPIRE", key, ttl)
end
return {1, newUsed, limit}
`)

	// Apply a signed delta to the running total (used to correct over-reservation).
	// Returns the new used value.
	luaAdjust := redis.NewScript(`
local key   = KEYS[1]
local delta = tonumber(ARGV[1])
local ttl   = tonumber(ARGV[2])

local used = tonumber(redis.call("GET", key) or "0")
used = math.max(0, used + delta)
redis.call("SET", key, used)
redis.call("EXPIRE", key, ttl)
return used
`)

	return &Store{
		rdb:        rdb,
		luaReserve: luaReserve,
		luaAdjust:  luaAdjust,
	}
}

// Reserve attempts to reserve amount tokens against limit under key.
// ttlSec is used as the Redis key TTL when the key is first created (i.e.
// first request of the day).
func (s *Store) Reserve(ctx context.Context, key string, limit, amount, ttlSec int) (ReserveResult, error) {
	vals, err := s.luaReserve.Run(ctx, s.rdb, []string{key}, limit, amount, ttlSec).Result()
	if err != nil {
		return ReserveResult{}, err
	}
	arr, ok := vals.([]any)
	if !ok || len(arr) < 3 {
		return ReserveResult{}, errors.New("quota: unexpected redis reserve result")
	}
	return ReserveResult{
		OK:    toInt(arr[0]) == 1,
		Used:  toInt(arr[1]),
		Limit: toInt(arr[2]),
	}, nil
}

// Adjust corrects the running total by delta (positive or negative).
// This is called after a response is received to replace the estimated
// reservation with the actual token count.
func (s *Store) Adjust(ctx context.Context, key string, delta, ttlSec int) (int, error) {
	v, err := s.luaAdjust.Run(ctx, s.rdb, []string{key}, delta, ttlSec).Result()
	if err != nil {
		return 0, err
	}
	return toInt(v), nil
}

// DayKey returns a Redis key suffix for the current UTC-local date.
// Callers pass now already converted to the quota timezone.
func DayKey(userID string, now time.Time) string {
	return "quota:" + userID + ":" +
		strconv.Itoa(now.Year()) +
		pad2(int(now.Month())) +
		pad2(now.Day())
}

// SecondsUntilMidnight returns the number of seconds until midnight in now's
// timezone, used as the Redis key TTL so that quotas reset daily.
func SecondsUntilMidnight(now time.Time) int {
	next := time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, now.Location())
	s := int(next.Sub(now).Seconds())
	if s < 1 {
		return 60
	}
	return s
}

// ---------- helpers ----------

func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

func toInt(v any) int {
	switch t := v.(type) {
	case int:
		return t
	case int64:
		return int(t)
	case float64:
		return int(t)
	case string:
		i, _ := strconv.Atoi(t)
		return i
	default:
		return 0
	}
}
