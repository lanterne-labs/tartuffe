-- +goose Up
-- A verdict on a detected contradiction, by a person or a model (reviewer).
-- The row keeps the two facts and the reason as they were reviewed: a reset,
-- an audit or a fold can delete or move the contradiction (contradiction_id
-- then goes NULL), and the precision of the detection stays measured by the
-- date it was detected. The person is kept by id and name without a foreign
-- key, so a person merge does not drop the history.
CREATE TABLE IF NOT EXISTS contradiction_reviews (
    id BIGSERIAL PRIMARY KEY,
    contradiction_id BIGINT REFERENCES contradictions(id) ON DELETE SET NULL,
    person_id BIGINT NOT NULL,
    person_name TEXT NOT NULL,
    fact_a_id BIGINT NOT NULL,
    fact_b_id BIGINT NOT NULL,
    fact_a_text TEXT NOT NULL,
    fact_b_text TEXT NOT NULL,
    reason TEXT NOT NULL,
    detected_at TIMESTAMPTZ NOT NULL,
    verdict TEXT NOT NULL CHECK (verdict IN ('real', 'debatable', 'false')),
    note TEXT NOT NULL DEFAULT '',
    reviewer TEXT NOT NULL,
    reviewed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (contradiction_id, reviewer)
);

CREATE INDEX IF NOT EXISTS idx_contradiction_reviews_detected_at ON contradiction_reviews(detected_at);

-- +goose Down
DROP TABLE IF EXISTS contradiction_reviews;
