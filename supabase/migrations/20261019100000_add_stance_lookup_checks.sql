-- +goose Up

-- The memo of the stance lookup of the merge sweep (core.lookupFragments):
-- for a small stance, the stance of the person that the LLM named as its
-- subject (NULL: none). The fingerprint covers the rulebook, the small
-- stance's label and summary, and the ids of the stances it could join; a
-- lookup runs again only when one of them changes.
CREATE TABLE IF NOT EXISTS stance_lookup_checks (
    stance_id   BIGINT PRIMARY KEY REFERENCES stance_documents(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    named_id    BIGINT,
    checked_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS stance_lookup_checks;
