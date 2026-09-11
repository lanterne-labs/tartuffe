-- +goose Up

-- summarized_at marks the moment the stance's label + summary last absorbed its
-- facts. Memberships added after it (stance_document_facts.added_at >
-- summarized_at) are the facts the next incremental refresh folds in; NULL means
-- nothing has been summarized yet (the next refresh folds every fact).
ALTER TABLE stance_documents ADD COLUMN IF NOT EXISTS summarized_at TIMESTAMPTZ;

CREATE INDEX IF NOT EXISTS idx_stance_document_facts_added_at
    ON stance_document_facts (stance_document_id, added_at);

-- +goose Down
DROP INDEX IF EXISTS idx_stance_document_facts_added_at;
ALTER TABLE stance_documents DROP COLUMN IF EXISTS summarized_at;
