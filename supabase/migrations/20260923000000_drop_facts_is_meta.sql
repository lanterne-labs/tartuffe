-- +goose Up
-- Synthesized meta-facts were migrated into claim groups with real parents
-- (dm facts migrate-metas, 2026-09-23): no row is a meta any more.
DROP INDEX IF EXISTS idx_facts_is_meta;
ALTER TABLE facts DROP COLUMN IF EXISTS is_meta;

-- +goose Down
ALTER TABLE facts ADD COLUMN IF NOT EXISTS is_meta BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS idx_facts_is_meta ON facts(id) WHERE is_meta;
