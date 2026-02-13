-- +goose Up
-- Add fact_pair_checks table to track all analyzed fact pairs
-- This prevents re-analyzing non-contradictory pairs on subsequent runs

CREATE TABLE IF NOT EXISTS fact_pair_checks (
    id BIGSERIAL PRIMARY KEY,
    person_id BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    fact_a_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    fact_b_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    is_contradiction BOOLEAN NOT NULL DEFAULT false,
    CONSTRAINT chk_pair_order CHECK (fact_a_id < fact_b_id),
    CONSTRAINT uq_fact_pair UNIQUE (person_id, fact_a_id, fact_b_id)
);

CREATE INDEX IF NOT EXISTS idx_fpc_person ON fact_pair_checks(person_id);
CREATE INDEX IF NOT EXISTS idx_fpc_fact_a ON fact_pair_checks(fact_a_id);
CREATE INDEX IF NOT EXISTS idx_fpc_fact_b ON fact_pair_checks(fact_b_id);

-- Backfill from existing contradictions
INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction, checked_at)
SELECT person_id, fact_a_id, fact_b_id, true, COALESCE(detected_at, created_at, NOW())
FROM contradictions
ON CONFLICT DO NOTHING;
