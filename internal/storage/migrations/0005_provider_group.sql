-- Providers are classified into three groups: custom endpoints configured by
-- hand, OAuth-token upstreams, and API-key upstreams picked from the catalogue.
-- Existing rows are custom: they were configured with an explicit base URL.
ALTER TABLE providers ADD COLUMN provider_group TEXT NOT NULL DEFAULT 'custom';

-- Catalogue entry a provider was created from, blank for custom providers.
ALTER TABLE providers ADD COLUMN catalog_id TEXT NOT NULL DEFAULT '';
