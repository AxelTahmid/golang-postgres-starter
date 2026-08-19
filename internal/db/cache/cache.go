// Package cache is the one shared cache mechanism in this service: a
// Postgres-backed key/value store over the UNLOGGED `cache` table, plus the
// atomic fixed-window counter that every rate limit is built on (see
// ratelimit.go). Using the database the service already runs means one less
// moving part than Redis, and it is the reason no feature needs a limiter
// table of its own.
//
// See migration 0003_cache_table.sql for the table's shape and why it is
// UNLOGGED.
package cache

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/AxelTahmid/tinker/internal/db/sqlc"
)

// maxTTLSeconds caps any caller-supplied window or TTL. It exists to keep the
// value provably inside int32 for the generated query parameter, and because a
// cache entry that outlives a month is not a cache entry.
const maxTTLSeconds = 31 * 24 * 60 * 60

// ErrInvalidTTL reports a TTL or window outside the supported range.
var ErrInvalidTTL = errors.New("cache: ttl must be between 1 and 2678400 seconds")

// Window is the state of one fixed-window counter after a hit.
type Window struct {
	// Hits is the number of hits recorded in the current window, including
	// the one that produced this value.
	Hits int
	// ExpiresAt is when the current window closes and the counter restarts.
	ExpiresAt time.Time
}

// RetryAfter reports how long is left on the window, rounded up to whole
// seconds and never below one second — a "wait 0 seconds" answer invites an
// immediate retry that is still blocked.
func (w Window) RetryAfter(now time.Time) time.Duration {
	remaining := w.ExpiresAt.Sub(now)
	if remaining <= 0 {
		return time.Second
	}
	if rounded := remaining.Truncate(time.Second); rounded == remaining {
		return rounded
	}
	return remaining.Truncate(time.Second) + time.Second
}

// Store is the slice of the cache that fixed-window rate limiting needs. It
// exists so Limiter can be unit-tested without a database.
type Store interface {
	IncrementWindow(ctx context.Context, key string, windowSeconds int) (Window, error)
	Flush(ctx context.Context, keys ...string) error
}

// Cache is the shared key/value cache.
type Cache struct {
	query *sqlc.Queries
}

// New builds a cache over the given queries handle.
func New(query *sqlc.Queries) *Cache {
	return &Cache{query: query}
}

var _ Store = (*Cache)(nil)

// Get returns the raw JSON stored under key, or nil when the key is absent or
// its entry has expired. An expired entry is indistinguishable from a miss by
// design: reads never observe a stale value, whether or not the purge job has
// reaped it yet.
func (c *Cache) Get(ctx context.Context, key string) ([]byte, error) {
	value, err := c.query.GetCacheEntry(ctx, key)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("cache get %q: %w", key, err)
	}
	return value, nil
}

// Set stores value under key for ttlSeconds. A ttlSeconds of 0 stores an entry
// that never expires — use it sparingly, since only expiring entries are
// reaped by the purge job.
func (c *Cache) Set(ctx context.Context, key string, value any, ttlSeconds int) error {
	if ttlSeconds < 0 || ttlSeconds > maxTTLSeconds {
		return ErrInvalidTTL
	}

	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("cache set %q: encoding value: %w", key, err)
	}

	if err = c.query.SetCacheEntry(ctx, sqlc.SetCacheEntryParams{
		Key:   key,
		Value: encoded,
		// #nosec G115 -- bounded above by maxTTLSeconds, well inside int32.
		TtlSeconds: int32(ttlSeconds),
	}); err != nil {
		return fmt.Errorf("cache set %q: %w", key, err)
	}
	return nil
}

// Increment bumps the fixed-window counter at key and returns its new value.
// The first hit, or a hit after the window lapsed, restarts the window at 1.
//
// The whole operation is a single atomic statement — see IncrementCacheCounter
// in internal/db/queries/cache.sql — so concurrent callers cannot lose a count
// between a read and a write.
func (c *Cache) Increment(ctx context.Context, key string, windowSeconds int) (int, error) {
	window, err := c.IncrementWindow(ctx, key, windowSeconds)
	if err != nil {
		return 0, err
	}
	return window.Hits, nil
}

// IncrementWindow is Increment with the window's closing instant attached, so
// callers can tell a client how long to wait. It is the same single statement.
func (c *Cache) IncrementWindow(ctx context.Context, key string, windowSeconds int) (Window, error) {
	if windowSeconds <= 0 || windowSeconds > maxTTLSeconds {
		return Window{}, ErrInvalidTTL
	}

	row, err := c.query.IncrementCacheCounter(ctx, sqlc.IncrementCacheCounterParams{
		Key: key,
		// #nosec G115 -- bounded above by maxTTLSeconds, well inside int32.
		WindowSeconds: int32(windowSeconds),
	})
	if err != nil {
		return Window{}, fmt.Errorf("cache increment %q: %w", key, err)
	}

	window := Window{Hits: int(row.Hits)}
	if row.ExpiresAt.Valid {
		window.ExpiresAt = row.ExpiresAt.Time
	}
	return window, nil
}

// Flush removes the named entries. Missing keys are not an error.
func (c *Cache) Flush(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	if err := c.query.DeleteCacheEntries(ctx, keys); err != nil {
		return fmt.Errorf("cache flush: %w", err)
	}
	return nil
}

// FlushPattern removes every entry whose key matches a glob-style pattern,
// e.g. "ratelimit:login:*".
func (c *Cache) FlushPattern(ctx context.Context, pattern string) error {
	if err := c.query.DeleteCacheEntriesByPattern(ctx, likePattern(pattern)); err != nil {
		return fmt.Errorf("cache flush pattern %q: %w", pattern, err)
	}
	return nil
}

// GetPattern lists the keys of every live entry matching a glob-style pattern.
func (c *Cache) GetPattern(ctx context.Context, pattern string) ([]string, error) {
	keys, err := c.query.ListCacheKeysByPattern(ctx, likePattern(pattern))
	if err != nil {
		return nil, fmt.Errorf("cache get pattern %q: %w", pattern, err)
	}
	return keys, nil
}

// PruneExpired deletes entries whose window has closed and reports how many
// went. Reads already ignore them; this is what stops the table growing
// forever. Wire it to a periodic River job.
func (c *Cache) PruneExpired(ctx context.Context) (int64, error) {
	deleted, err := c.query.PurgeExpiredCacheEntries(ctx)
	if err != nil {
		return 0, fmt.Errorf("cache prune expired: %w", err)
	}
	return deleted, nil
}

// likePattern converts the glob-style '*' callers write into the SQL LIKE
// wildcard. Keys are namespaced and machine-generated, so no other LIKE
// metacharacter needs escaping.
func likePattern(pattern string) string {
	return strings.ReplaceAll(pattern, "*", "%")
}
