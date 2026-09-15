-- Capabilities advertised by a model alias: chat, vision, tools, embeddings,
-- reasoning, streaming, audio and json_mode, stored as a comma-separated list
-- in the same shape as fallback chains. Pre-existing aliases carry an empty
-- list, which reads as "not yet classified" rather than "supports nothing" —
-- they are filled in on the next edit or re-import.
ALTER TABLE model_aliases ADD COLUMN capabilities TEXT NOT NULL DEFAULT '';
