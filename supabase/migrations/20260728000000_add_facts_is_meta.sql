-- +goose Up
-- Meta-facts: an LLM-synthesized fact that aggregates several near-identical
-- member facts of the same person. Members link to their meta via the existing
-- fact_relations 'duplicate' relation (general = meta, specific = member), so
-- every active-fact exclusion, ancestor redirect, and contradiction repoint
-- works unchanged. is_meta marks the synthesized row itself.
ALTER TABLE facts ADD COLUMN IF NOT EXISTS is_meta BOOLEAN NOT NULL DEFAULT false;
CREATE INDEX IF NOT EXISTS idx_facts_is_meta ON facts(id) WHERE is_meta;

-- +goose Down
DROP INDEX IF EXISTS idx_facts_is_meta;
ALTER TABLE facts DROP COLUMN IF EXISTS is_meta;
