-- Routing configuration moves into the database: it is the single source of
-- truth for providers and model aliases, managed through the admin API.
ALTER TABLE providers ADD COLUMN timeout_ms INTEGER NOT NULL DEFAULT 0;

ALTER TABLE providers ADD COLUMN kind TEXT NOT NULL DEFAULT 'openai';

ALTER TABLE model_aliases ADD COLUMN fallback TEXT NOT NULL DEFAULT '';

CREATE TABLE IF NOT EXISTS settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);
