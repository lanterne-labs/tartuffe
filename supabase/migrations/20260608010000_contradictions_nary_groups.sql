-- +goose Up
-- N-ary contradiction GROUPS: one base (anchor) fact contradicted by N facts.
--
-- The contradictions table stays strictly pairwise and canonical (fact_a_id <
-- fact_b_id is preserved, so insert/dedup/repoint are unchanged and the unique
-- constraint keeps preventing duplicate pairs). Grouping is layered on top via a
-- nullable base_fact_id pointer set by the regroup pass: a group = all rows sharing
-- (person_id, base_fact_id). The contradicting fact of a row is whichever of
-- {fact_a_id, fact_b_id} is not the base. base_fact_id is always one of the row's
-- two facts; NULL means "not yet regrouped" (reads fall back to fact_a_id as base).
ALTER TABLE contradictions ADD COLUMN IF NOT EXISTS base_fact_id BIGINT REFERENCES facts(id) ON DELETE CASCADE;

-- Persisted LLM-synthesized merged reason for a group, denormalized across every
-- row sharing a base (the regroup pass is the sole writer, so the copies stay
-- consistent). NULL for single-fact groups (the row's own reason suffices).
ALTER TABLE contradictions ADD COLUMN IF NOT EXISTS group_summary TEXT;
ALTER TABLE contradictions ADD COLUMN IF NOT EXISTS group_summary_en TEXT;

-- Group reads and the regroup pass scan by (person_id, base fact).
CREATE INDEX IF NOT EXISTS idx_contradictions_base_fact ON contradictions (person_id, base_fact_id);

-- +goose Down
DROP INDEX IF EXISTS idx_contradictions_base_fact;
ALTER TABLE contradictions DROP COLUMN IF EXISTS group_summary_en;
ALTER TABLE contradictions DROP COLUMN IF EXISTS group_summary;
ALTER TABLE contradictions DROP COLUMN IF EXISTS base_fact_id;
