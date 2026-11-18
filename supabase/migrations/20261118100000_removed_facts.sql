-- +goose Up
-- The facts a person's decision removed (an audit's cleanup, an accepted
-- report, a complaint): what was removed from which source, so the same fact
-- is not stored again when its document is extracted again (a page cut
-- mid-extraction, a rebuild). The fact inserter reads it (core.UseRemovedFacts).
CREATE TABLE IF NOT EXISTS removed_facts (
    id         BIGSERIAL PRIMARY KEY,
    fact_id    BIGINT NOT NULL,
    source_url TEXT NOT NULL,
    fact_text  TEXT NOT NULL,
    quote      TEXT NOT NULL DEFAULT '',
    reason     TEXT NOT NULL DEFAULT '',
    removed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (fact_id, source_url)
);
CREATE INDEX IF NOT EXISTS removed_facts_source_url ON removed_facts (source_url);

-- +goose Down
DROP TABLE IF EXISTS removed_facts;
