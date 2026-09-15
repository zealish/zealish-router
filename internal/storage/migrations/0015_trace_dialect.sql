-- Which wire dialect the *client* spoke. The gateway accepts both OpenAI
-- (/v1/chat/completions, /v1/embeddings) and Anthropic (/v1/messages) and
-- routes them onto the same aliases, so without this column the dashboard
-- cannot tell the two populations apart.
--
-- The upstream dialect is already visible through the attempt's provider, so
-- only the inbound one is stored. Rows written before this migration predate
-- the Anthropic endpoint and are therefore all OpenAI, which is the default.
ALTER TABLE request_traces ADD COLUMN dialect TEXT NOT NULL DEFAULT 'openai';
