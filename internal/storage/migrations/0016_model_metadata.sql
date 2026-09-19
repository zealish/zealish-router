-- Static metadata for model aliases. Existing rows retain zero values, which
-- represent unknown metadata and preserve compatibility with legacy records.
ALTER TABLE model_aliases ADD COLUMN max_context INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_aliases ADD COLUMN quality_tier INTEGER NOT NULL DEFAULT 0;
ALTER TABLE model_aliases ADD COLUMN price_input REAL NOT NULL DEFAULT 0;
ALTER TABLE model_aliases ADD COLUMN price_output REAL NOT NULL DEFAULT 0;
