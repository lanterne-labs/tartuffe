-- +goose Up
-- Add support for canonical and related facts
-- Related facts are linked to a canonical fact and are not used for contradiction detection

-- Add is_canonical flag to facts table (defaults to true for existing facts)
ALTER TABLE facts
ADD COLUMN is_canonical BOOLEAN NOT NULL DEFAULT true;

-- Add canonical_fact_id to facts table for related facts
-- NULL for canonical facts, FK to canonical fact for related facts
ALTER TABLE facts
ADD COLUMN canonical_fact_id BIGINT REFERENCES facts(id) ON DELETE CASCADE;

-- Add constraint: canonical facts must have NULL canonical_fact_id
-- Related facts must have non-NULL canonical_fact_id
ALTER TABLE facts
ADD CONSTRAINT canonical_fact_constraint
CHECK (
  (is_canonical = true AND canonical_fact_id IS NULL) OR
  (is_canonical = false AND canonical_fact_id IS NOT NULL)
);

-- Index for finding related facts by canonical fact ID
CREATE INDEX idx_facts_canonical_id ON facts(canonical_fact_id) WHERE canonical_fact_id IS NOT NULL;

-- Index for finding canonical facts only (used in contradiction detection)
CREATE INDEX idx_facts_canonical ON facts(is_canonical) WHERE is_canonical = true;

-- Drop UNIQUE constraint on fact_text (we'll allow duplicates now)
-- Similar facts can have different text but will be linked via canonical_fact_id
ALTER TABLE facts DROP CONSTRAINT IF EXISTS facts_fact_text_key;

-- Add comment for documentation
COMMENT ON COLUMN facts.is_canonical IS 'True if this is a canonical fact used for contradiction detection. False if this is a related/similar fact.';
COMMENT ON COLUMN facts.canonical_fact_id IS 'For related facts, points to the canonical fact. NULL for canonical facts.';
