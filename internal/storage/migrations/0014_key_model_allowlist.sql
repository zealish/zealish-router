-- Per-key model allowlist. An empty column means the key may address every
-- alias and combo, so existing keys keep their current reach after the
-- migration. Entries are comma-separated model names, like fallback chains.
ALTER TABLE api_keys ADD COLUMN allowed_models TEXT NOT NULL DEFAULT '';
