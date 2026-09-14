-- Per-key quotas. Zero means unlimited on both columns, so existing keys keep
-- their current unrestricted behaviour after the migration.
ALTER TABLE api_keys ADD COLUMN rate_limit_per_min INTEGER NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN monthly_budget_usd REAL NOT NULL DEFAULT 0;
