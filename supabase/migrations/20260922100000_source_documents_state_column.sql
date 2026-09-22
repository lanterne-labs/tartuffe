-- +goose Up

-- Corrective. A first draft of 20260922000000 (an extraction_status column,
-- no debate_documents move) reached prod through a laptop-run dm command
-- before the file was final, and goose then took the version as applied.
-- This brings any database to the final shape and is a no-op where the
-- final version ran: the state column, the unfinished index, the
-- debate_documents markers moved over, the old table dropped.
ALTER TABLE source_documents DROP COLUMN IF EXISTS extraction_status;
ALTER TABLE source_documents ADD COLUMN IF NOT EXISTS state TEXT NOT NULL DEFAULT 'fetched';
DROP INDEX IF EXISTS idx_source_documents_pending;
CREATE INDEX IF NOT EXISTS idx_source_documents_unfinished
    ON source_documents (source_type, person_id) WHERE state IN ('fetched', 'failed');

-- +goose StatementBegin
DO $$
BEGIN
    IF to_regclass('debate_documents') IS NOT NULL THEN
        INSERT INTO source_documents (source_type, url, person_id, title, content, published_at, metadata, fetched_at, state, attempts, extracted_at, facts_count)
        SELECT 'assemblee_debat', 'debats:' || d.cr_uid || '/' || d.actor_id, d.person_id, '', '', d.seance_date,
               jsonb_build_object('source_url', d.source_url, 'cr_uid', d.cr_uid, 'actor_id', d.actor_id, 'paragraphs', d.paragraphs, 'chars', d.chars, 'facts_dropped', d.facts_dropped),
               d.processed_at, 'extracted', 1, d.processed_at, d.facts_count
        FROM debate_documents d
        ON CONFLICT (source_type, url) DO NOTHING;
        DROP TABLE debate_documents;
    END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
-- Nothing to undo: 20260922000000's Down restores debate_documents.
SELECT 1;
