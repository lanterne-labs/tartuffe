-- +goose Up

-- Durable count of extractor facts discarded because their subject was not
-- the speaker (cron logs are not retrievable after the fact); the product
-- decision on group-attributed facts ("notre groupe demande…") needs it.
ALTER TABLE debate_documents ADD COLUMN IF NOT EXISTS facts_dropped INT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE debate_documents DROP COLUMN IF EXISTS facts_dropped;
