-- +goose Up
CREATE TABLE IF NOT EXISTS contradiction_votes (
    id            BIGSERIAL PRIMARY KEY,
    base_fact_id  BIGINT NOT NULL REFERENCES facts(id) ON DELETE CASCADE,
    ip_hash       TEXT NOT NULL,
    vote          TEXT NOT NULL CHECK (vote IN ('agree', 'disagree', 'not_a_contradiction')),
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- One vote per (group, IP). Upsert replaces on conflict.
CREATE UNIQUE INDEX IF NOT EXISTS contradiction_votes_fact_ip
    ON contradiction_votes (base_fact_id, ip_hash);

-- Index for batch count queries.
CREATE INDEX IF NOT EXISTS contradiction_votes_fact_id
    ON contradiction_votes (base_fact_id);

-- +goose Down
DROP TABLE IF EXISTS contradiction_votes;
