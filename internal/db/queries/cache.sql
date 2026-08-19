-- Queries for the shared UNLOGGED `cache` table. Every one of them treats an
-- expired row as absent rather than deleting it inline; reaping is the purge
-- job's business.
-- GetCacheEntry reads a live entry. An expired row matches no predicate here,
-- so a caller can never observe a stale value even before the purge runs.
-- name: GetCacheEntry :one
SELECT
    value
FROM
    cache
WHERE
    key = @key
    AND (
        expires_at IS NULL
        OR expires_at > NOW()
    );

-- SetCacheEntry writes or replaces an entry. A ttl_seconds of 0 stores an entry
-- that never expires; created_at is refreshed because a replaced value is a new
-- entry, not an update to the old one.
-- name: SetCacheEntry :exec
INSERT INTO
    cache (key, value, expires_at)
VALUES
    (
        @key,
        @value,
        CASE
            WHEN @ttl_seconds::INT > 0 THEN NOW() + MAKE_INTERVAL(secs => @ttl_seconds::INT)
            ELSE NULL
        END
    )
ON CONFLICT (key) DO UPDATE
SET
    value = EXCLUDED.value,
    expires_at = EXCLUDED.expires_at,
    created_at = NOW();

-- IncrementCacheCounter bumps a fixed-window counter and returns its new value
-- together with the instant the window closes.
--
-- The upsert and the conditional window reset are ONE statement on purpose. A
-- read-then-write loses increments under concurrency — two callers both read N
-- and both write N+1 — which is precisely the failure a rate limiter must not
-- have. Here the ON CONFLICT path takes a row lock, so concurrent callers are
-- serialized by Postgres and every hit is counted exactly once.
--
-- The first hit, or a hit arriving after the window lapsed, restarts the window
-- at 1. A hit inside a live window increments and leaves expires_at alone, so a
-- blocked caller cannot extend their own penalty window by retrying.
-- name: IncrementCacheCounter :one
INSERT INTO
    cache (key, value, expires_at)
VALUES
    (
        @key,
        '1'::JSONB,
        NOW() + MAKE_INTERVAL(secs => @window_seconds::INT)
    )
ON CONFLICT (key) DO UPDATE
SET
    value = CASE
        WHEN cache.expires_at IS NULL
        OR cache.expires_at <= NOW() THEN '1'::JSONB
        ELSE TO_JSONB((cache.value)::TEXT::INT + 1)
    END,
    expires_at = CASE
        WHEN cache.expires_at IS NULL
        OR cache.expires_at <= NOW() THEN NOW() + MAKE_INTERVAL(secs => @window_seconds::INT)
        ELSE cache.expires_at
    END,
    created_at = cache.created_at
RETURNING
    (value)::TEXT::INT AS hits,
    expires_at;

-- name: DeleteCacheEntries :exec
DELETE FROM cache
WHERE
    key = ANY (@keys::TEXT[]);

-- DeleteCacheEntriesByPattern takes a SQL LIKE pattern; callers translate the
-- glob-style '*' they use elsewhere into '%'.
-- name: DeleteCacheEntriesByPattern :exec
DELETE FROM cache
WHERE
    key LIKE @pattern;

-- name: ListCacheKeysByPattern :many
SELECT
    key
FROM
    cache
WHERE
    key LIKE @pattern
    AND (
        expires_at IS NULL
        OR expires_at > NOW()
    )
ORDER BY
    key;

-- PurgeExpiredCacheEntries reaps rows the reads already ignore. Without it the
-- table grows forever, since a lapsed rate-limit window is never revisited.
-- name: PurgeExpiredCacheEntries :execrows
DELETE FROM cache
WHERE
    expires_at IS NOT NULL
    AND expires_at <= NOW();
