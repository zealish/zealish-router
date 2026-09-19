-- Providers may hold multiple upstream credentials. JSON keeps ordering stable;
-- the legacy api_key column remains the primary credential for compatibility.
ALTER TABLE providers ADD COLUMN api_keys TEXT NOT NULL DEFAULT '[]';
ALTER TABLE providers ADD COLUMN api_key_method TEXT NOT NULL DEFAULT 'off';
