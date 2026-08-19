-- +goose Up
-- +goose StatementBegin
-- UNLOGGED key/value cache. Skipping the WAL makes writes cheap, and the
-- contents being lost on a crash is exactly right for a cache. This is the ONE
-- shared cache mechanism: general memoisation plus the atomic fixed-window
-- counter that backs every rate limit, so no feature has to invent a limiter
-- table of its own. Using the database already in the stack means one less
-- moving part than Redis.
CREATE UNLOGGED TABLE cache (
    key TEXT PRIMARY KEY,
    value JSONB NOT NULL,
    expires_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP
);

COMMENT ON TABLE cache IS 'Shared UNLOGGED key/value cache and fixed-window rate-limit counters.';

COMMENT ON COLUMN cache.key IS 'Opaque namespaced key. Rate-limit keys embed a sha256 digest of the identifier, never a raw email or IP.';

COMMENT ON COLUMN cache.expires_at IS 'NULL means the entry never expires. Expired rows stay readable-as-absent until the purge job reaps them.';

-- Serves the purge job's sweep. The per-read liveness predicate is answered
-- from the primary key lookup, so this index exists for that scan alone.
CREATE INDEX idx_cache_expires_at ON cache (expires_at);

-- +goose StatementEnd
-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_cache_expires_at;

DROP TABLE IF EXISTS cache;

-- +goose StatementEnd
