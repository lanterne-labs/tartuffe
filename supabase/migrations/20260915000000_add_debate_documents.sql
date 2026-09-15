-- +goose Up

-- One row per (séance, speaker) transcript document the débats ingestion has
-- processed, whether or not it yielded facts. Identity is the stable pair
-- (compte-rendu uid, AN actor id) — NOT the source URL, whose paragraph anchor
-- moves when the substantive-paragraph threshold changes or the AN republishes
-- a séance. The pending set of the nightly step is "documents in the AN
-- archive whose (cr_uid, actor_id) is not here", so a document that produced
-- zero facts is never re-extracted.
CREATE TABLE IF NOT EXISTS debate_documents (
    cr_uid       TEXT NOT NULL,
    actor_id     TEXT NOT NULL,
    source_url   TEXT NOT NULL,
    person_id    BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    seance_date  DATE NOT NULL,
    paragraphs   INT NOT NULL,
    chars        INT NOT NULL,
    facts_count  INT NOT NULL DEFAULT 0,
    processed_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (cr_uid, actor_id)
);

CREATE INDEX IF NOT EXISTS idx_debate_documents_seance_date ON debate_documents (seance_date);
CREATE INDEX IF NOT EXISTS idx_debate_documents_person_id ON debate_documents (person_id);

-- +goose Down
DROP TABLE IF EXISTS debate_documents;
