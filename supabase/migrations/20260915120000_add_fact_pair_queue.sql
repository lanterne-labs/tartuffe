-- +goose Up

-- Generic queue of fact pairs a producer wants the contradiction detector to
-- judge, regardless of embedding proximity. Entries are consumed (taken and
-- deleted) by the nightly detection and judged through the normal pair path,
-- which records the verdict in fact_pair_checks — the queue is transient by
-- design.
CREATE TABLE IF NOT EXISTS fact_pair_queue (
    person_id   BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    fact_a_id   BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    fact_b_id   BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    source      TEXT NOT NULL,
    enqueued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (person_id, fact_a_id, fact_b_id),
    CHECK (fact_a_id < fact_b_id)
);

CREATE INDEX IF NOT EXISTS idx_fact_pair_queue_fact_a ON fact_pair_queue (fact_a_id);
CREATE INDEX IF NOT EXISTS idx_fact_pair_queue_fact_b ON fact_pair_queue (fact_b_id);

-- +goose Down
DROP INDEX IF EXISTS idx_fact_pair_queue_fact_b;
DROP INDEX IF EXISTS idx_fact_pair_queue_fact_a;
DROP TABLE IF EXISTS fact_pair_queue;
