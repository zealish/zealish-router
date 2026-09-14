-- Attribute every usage event to the API key that paid for it. Rows written
-- before this migration keep an empty key_id, which reads as "unattributed".
ALTER TABLE usage_events ADD COLUMN key_id TEXT NOT NULL DEFAULT '';

-- Per-key spend and rate windows both scan by key within a time range.
CREATE INDEX IF NOT EXISTS idx_usage_events_key_created
    ON usage_events (key_id, created_at DESC);
