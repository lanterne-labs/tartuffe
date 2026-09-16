-- +goose Up

-- Memo of the nightly stance merge sweep: the fingerprint of a person's stance
-- set (ids, labels, summaries) plus the merge rulebook at the time the sweep
-- last found nothing to merge. The sweep skips a person whose live fingerprint
-- still matches, so the LLM is only asked again when a stance changed (a new
-- fact, a refreshed summary, a merge) or the rulebook changed. Rows for
-- persons that merged are refreshed with the post-merge fingerprint.
CREATE TABLE IF NOT EXISTS stance_merge_checks (
    person_id   BIGINT PRIMARY KEY REFERENCES persons(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,
    checked_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- +goose Down
DROP TABLE IF EXISTS stance_merge_checks;
