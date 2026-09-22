-- +goose Up

-- The document state machine shared by every plugin (see core.SourceDocument):
-- a fetched document is saved the moment the fetch succeeds, with its raw
-- content; extraction moves it to extracted (facts_count, content deleted)
-- or failed (retried until the attempt cap). The row outlives the content
-- as the "never fetch this URL again" marker. Before this table a paid
-- scrape lived only in memory: an article whose extraction failed, or
-- yielded no facts, left no trace and was scraped and extracted again every
-- night while the search window still returned it (run 104, 2026-09-22: a
-- 77-minute tunnel outage cost the whole night's scrapes).
CREATE TABLE IF NOT EXISTS source_documents (
    id                BIGSERIAL PRIMARY KEY,
    source_type       TEXT NOT NULL,
    url               TEXT NOT NULL,
    person_id         BIGINT REFERENCES persons(id) ON DELETE SET NULL,
    title             TEXT NOT NULL DEFAULT '',
    content           TEXT NOT NULL,
    published_at      TIMESTAMPTZ,
    metadata          JSONB NOT NULL DEFAULT '{}'::jsonb,
    fetched_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    state             TEXT NOT NULL DEFAULT 'fetched',
    attempts          INT NOT NULL DEFAULT 0,
    extracted_at      TIMESTAMPTZ,
    facts_count       INT NOT NULL DEFAULT 0,
    last_error        TEXT,
    UNIQUE (source_type, url)
);

CREATE INDEX IF NOT EXISTS idx_source_documents_unfinished
    ON source_documents (source_type, person_id) WHERE state IN ('fetched', 'failed');
CREATE INDEX IF NOT EXISTS idx_source_documents_url ON source_documents (url);

-- The débats step kept its own version of this machine (debate_documents,
-- one processed marker per (séance, speaker)); those markers move over as
-- extracted documents keyed by that identity, so nothing is re-extracted.
INSERT INTO source_documents (source_type, url, person_id, title, content, published_at, metadata, fetched_at, state, attempts, extracted_at, facts_count)
SELECT 'assemblee_debat', 'debats:' || d.cr_uid || '/' || d.actor_id, d.person_id, '', '', d.seance_date,
       jsonb_build_object('source_url', d.source_url, 'cr_uid', d.cr_uid, 'actor_id', d.actor_id, 'paragraphs', d.paragraphs, 'chars', d.chars, 'facts_dropped', d.facts_dropped),
       d.processed_at, 'extracted', 1, d.processed_at, d.facts_count
FROM debate_documents d
ON CONFLICT (source_type, url) DO NOTHING;
DROP TABLE IF EXISTS debate_documents;

-- +goose Down
CREATE TABLE IF NOT EXISTS debate_documents (
    cr_uid        TEXT NOT NULL,
    actor_id      TEXT NOT NULL,
    source_url    TEXT NOT NULL,
    person_id     BIGINT NOT NULL REFERENCES persons(id) ON DELETE CASCADE,
    seance_date   DATE NOT NULL,
    paragraphs    INT NOT NULL,
    chars         INT NOT NULL,
    facts_count   INT NOT NULL DEFAULT 0,
    facts_dropped INT NOT NULL DEFAULT 0,
    processed_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (cr_uid, actor_id)
);
INSERT INTO debate_documents (cr_uid, actor_id, source_url, person_id, seance_date, paragraphs, chars, facts_count, facts_dropped, processed_at)
SELECT metadata->>'cr_uid', metadata->>'actor_id', COALESCE(metadata->>'source_url', url), person_id, published_at::date,
       COALESCE((metadata->>'paragraphs')::int, 0), COALESCE((metadata->>'chars')::int, 0), facts_count,
       COALESCE((metadata->>'facts_dropped')::int, 0), COALESCE(extracted_at, fetched_at)
FROM source_documents
WHERE source_type = 'assemblee_debat' AND state = 'extracted' AND person_id IS NOT NULL
ON CONFLICT (cr_uid, actor_id) DO NOTHING;
DROP TABLE IF EXISTS source_documents;
