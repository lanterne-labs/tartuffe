-- +goose Up
ALTER TABLE facts ADD COLUMN IF NOT EXISTS canonical_verb TEXT;
CREATE INDEX IF NOT EXISTS idx_facts_canonical_verb ON facts (canonical_verb);
ALTER TABLE facts DROP COLUMN IF EXISTS topics;
