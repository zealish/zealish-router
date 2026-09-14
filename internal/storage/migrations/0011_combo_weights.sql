-- Per-member weights for the 'weighted' combo strategy, stored positionally
-- alongside members. An empty string means "unweighted", which is what every
-- pre-existing combo and every non-weighted strategy carries.
ALTER TABLE combos ADD COLUMN weights TEXT NOT NULL DEFAULT '';
