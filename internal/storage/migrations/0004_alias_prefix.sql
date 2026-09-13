-- Providers carry a default alias prefix so imported model aliases are
-- namespaced per provider and cannot collide across upstreams.
ALTER TABLE providers ADD COLUMN alias_prefix TEXT NOT NULL DEFAULT '';
