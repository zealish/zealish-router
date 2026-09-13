-- Combos are virtual models: one client-facing name backed by an ordered pool
-- of aliases, tried according to a routing strategy.
CREATE TABLE IF NOT EXISTS combos (
    name     TEXT    PRIMARY KEY,
    strategy TEXT    NOT NULL DEFAULT 'fallback',
    members  TEXT    NOT NULL DEFAULT '',
    enabled  INTEGER NOT NULL DEFAULT 1
);
