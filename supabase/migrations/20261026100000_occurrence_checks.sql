-- +goose Up
-- The verdicts of the grouping gate (core.FactDedupeAdjudicator) on fact
-- pairs, under the gate's prompt (rule_hash). The insertion compares a new
-- fact with its 5 nearest groups only, so two groups of one occurrence could
-- form and never be compared; the fold asks the pairs no verdict covers and
-- reuses the others.
CREATE TABLE IF NOT EXISTS occurrence_checks (
    fact_a_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    fact_b_id BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    rule_hash TEXT NOT NULL,
    verdict TEXT NOT NULL,
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (fact_a_id, fact_b_id),
    CHECK (fact_a_id < fact_b_id)
);
CREATE INDEX IF NOT EXISTS occurrence_checks_fact_b_idx ON occurrence_checks (fact_b_id);

-- The fold of a person's active facts under a gate prompt: the person is
-- folded again once their active facts change (fingerprint) or the prompt
-- does.
CREATE TABLE IF NOT EXISTS fact_fold_checks (
    person_id BIGINT PRIMARY KEY REFERENCES persons(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    checked_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS fact_fold_checks;
DROP TABLE IF EXISTS occurrence_checks;
