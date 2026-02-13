-- +goose Up
-- Create the fact_relations table for tracking duplicate and implication relationships between facts.
-- Replaces the legacy canonical_fact_id / is_canonical system on the facts table.

CREATE TABLE IF NOT EXISTS fact_relations (
    id BIGSERIAL PRIMARY KEY,
    general_fact_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    specific_fact_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    relation_type TEXT NOT NULL CHECK (relation_type IN ('duplicate', 'implication')),
    reason TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT uq_fact_relation UNIQUE (general_fact_id, specific_fact_id),
    CONSTRAINT chk_no_self_relation CHECK (general_fact_id <> specific_fact_id)
);

-- Indexes for chain walking and lookups
CREATE INDEX IF NOT EXISTS idx_fact_relations_general ON fact_relations(general_fact_id);
CREATE INDEX IF NOT EXISTS idx_fact_relations_specific ON fact_relations(specific_fact_id);
CREATE INDEX IF NOT EXISTS idx_fact_relations_type ON fact_relations(relation_type);

-- Migrate existing canonical_fact_id data into fact_relations as 'duplicate' relations
INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type, reason)
SELECT canonical_fact_id, id, 'duplicate', 'Migrated from legacy canonical system'
FROM facts
WHERE is_canonical = false AND canonical_fact_id IS NOT NULL
ON CONFLICT DO NOTHING;

-- Drop legacy columns (no backward compat needed - nothing in production)
ALTER TABLE facts DROP CONSTRAINT IF EXISTS canonical_fact_constraint;
ALTER TABLE facts DROP COLUMN IF EXISTS is_canonical;
ALTER TABLE facts DROP COLUMN IF EXISTS canonical_fact_id;
DROP INDEX IF EXISTS idx_facts_canonical_id;
DROP INDEX IF EXISTS idx_facts_canonical;
