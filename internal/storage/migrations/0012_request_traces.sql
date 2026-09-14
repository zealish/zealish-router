-- Per-request trace. One row per gateway request, however many providers the
-- fallback chain had to walk, plus one attempt row per upstream call. The
-- usage log answers "what did this cost"; the trace answers "why did it take
-- this route". Neither table stores prompt or completion content: metadata
-- only, so a trace stays cheap to keep and safe to expose to the dashboard.
CREATE TABLE IF NOT EXISTS request_traces (
    request_id     TEXT    PRIMARY KEY,
    created_at     INTEGER NOT NULL,
    key_id         TEXT    NOT NULL DEFAULT '',
    model          TEXT    NOT NULL,
    streamed       INTEGER NOT NULL DEFAULT 0,
    total_latency_ms INTEGER NOT NULL DEFAULT 0,
    total_tokens   INTEGER NOT NULL DEFAULT 0,
    total_cost_usd REAL    NOT NULL DEFAULT 0,
    final_provider TEXT    NOT NULL DEFAULT '',
    final_alias    TEXT    NOT NULL DEFAULT '',
    final_status   TEXT    NOT NULL DEFAULT '',
    attempts       INTEGER NOT NULL DEFAULT 0
);

-- The list view always reads newest-first, and every filter it offers narrows
-- that same scan.
CREATE INDEX IF NOT EXISTS idx_request_traces_created_at
    ON request_traces (created_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_traces_status_created
    ON request_traces (final_status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_request_traces_key_created
    ON request_traces (key_id, created_at DESC);

-- One row per upstream call, in the order the chain tried them. Seq starts at
-- 1 and is unique within a trace, so the timeline needs no sort key of its own.
CREATE TABLE IF NOT EXISTS request_attempts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id TEXT    NOT NULL,
    seq        INTEGER NOT NULL,
    started_at INTEGER NOT NULL,
    alias      TEXT    NOT NULL,
    provider   TEXT    NOT NULL,
    model      TEXT    NOT NULL,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    status     TEXT    NOT NULL,
    -- retry marks a repeat of the same route, fallback a move to the next one.
    retry      INTEGER NOT NULL DEFAULT 0,
    fallback   INTEGER NOT NULL DEFAULT 0,
    error      TEXT    NOT NULL DEFAULT '',
    UNIQUE (request_id, seq),
    FOREIGN KEY (request_id) REFERENCES request_traces (request_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_request_attempts_request
    ON request_attempts (request_id, seq);

-- Filtering the list by provider or upstream model reaches through the
-- attempts, so that lookup gets its own index.
CREATE INDEX IF NOT EXISTS idx_request_attempts_provider
    ON request_attempts (provider);
