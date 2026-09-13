-- Per-request usage log. Prometheus counters live in memory and reset with the
-- process, so the dashboard's totals, recent-request list and time series read
-- from here instead.
CREATE TABLE IF NOT EXISTS usage_events (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    created_at        INTEGER NOT NULL,
    alias             TEXT    NOT NULL,
    provider          TEXT    NOT NULL,
    model             TEXT    NOT NULL,
    streamed          INTEGER NOT NULL DEFAULT 0,
    status            TEXT    NOT NULL DEFAULT 'ok',
    duration_ms       INTEGER NOT NULL DEFAULT 0,
    prompt_tokens     INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    cached_tokens     INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    reasoning_tokens  INTEGER NOT NULL DEFAULT 0,
    cost_usd          REAL    NOT NULL DEFAULT 0
);

-- The dashboard always reads newest-first and buckets by time.
CREATE INDEX IF NOT EXISTS idx_usage_events_created_at
    ON usage_events (created_at DESC);
