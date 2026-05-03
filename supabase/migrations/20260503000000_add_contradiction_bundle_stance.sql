-- +goose Up
-- Stance-bundled contradictions
--
-- A contradiction's "bundle" is the stance its facts belong to. When fact_a and fact_b
-- share a stance, that stance becomes the bundle id (denormalised here for fast reads).
-- When facts are in different stances or pre-date the stance backfill, bundle_stance_id
-- is NULL — those rows are treated as singleton "solo" bundles in the score formula.
--
-- The Bundler component (backend/contradiction/bundler.go) may upgrade NULL bundles via
-- an LLM-verified embedding lookup over per-person stance centroids; that upgrade simply
-- writes a non-null value into this column.

ALTER TABLE contradictions
    ADD COLUMN IF NOT EXISTS bundle_stance_id BIGINT
    REFERENCES stance_documents(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_contradictions_bundle_stance
    ON contradictions(person_id, bundle_stance_id);

-- Fast-path backfill: where both facts already share a stance via stance_document_facts,
-- record that stance as the bundle. Cross-stance and unstanced rows are left NULL and
-- behave as solo bundles until an explicit bundler pass is run.
WITH shared AS (
    SELECT c.id AS contradiction_id, sa.stance_document_id
    FROM contradictions c
    JOIN stance_document_facts sa ON sa.fact_id = c.fact_a_id
    JOIN stance_document_facts sb ON sb.fact_id = c.fact_b_id
                                  AND sb.stance_document_id = sa.stance_document_id
)
UPDATE contradictions c
SET bundle_stance_id = shared.stance_document_id
FROM shared
WHERE shared.contradiction_id = c.id
  AND c.bundle_stance_id IS NULL;
