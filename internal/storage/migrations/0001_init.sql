CREATE TABLE IF NOT EXISTS api_keys (
    id           TEXT    PRIMARY KEY,
    name         TEXT    NOT NULL,
    key_hash     TEXT    NOT NULL UNIQUE,
    enabled      INTEGER NOT NULL DEFAULT 1,
    created_at   INTEGER NOT NULL,
    last_used_at INTEGER NOT NULL DEFAULT 0
);

CREATE INDEX IF NOT EXISTS idx_api_keys_hash ON api_keys (key_hash);

CREATE TABLE IF NOT EXISTS providers (
    id       TEXT    PRIMARY KEY,
    name     TEXT    NOT NULL UNIQUE,
    base_url TEXT    NOT NULL,
    api_key  TEXT    NOT NULL DEFAULT '',
    enabled  INTEGER NOT NULL DEFAULT 1
);

CREATE TABLE IF NOT EXISTS model_aliases (
    alias    TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    model    TEXT NOT NULL
);
