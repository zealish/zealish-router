-- The proxy pool holds outbound proxy endpoints. Providers opt into the pool
-- with use_proxy_pool; enabled proxies are rotated per request.
CREATE TABLE IF NOT EXISTS proxies (
    name    TEXT    PRIMARY KEY,
    url     TEXT    NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1
);

ALTER TABLE providers ADD COLUMN use_proxy_pool INTEGER NOT NULL DEFAULT 0;
