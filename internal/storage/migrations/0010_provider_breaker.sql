-- Per-provider circuit breaker overrides. NULL means "inherit the global
-- policy from config.yaml", which is why these columns are nullable rather
-- than carrying a DEFAULT: 0 is a meaningful value (breaker disabled), so it
-- cannot double as "unset".
ALTER TABLE providers ADD COLUMN breaker_threshold INTEGER;
ALTER TABLE providers ADD COLUMN breaker_cooldown_ms INTEGER;
