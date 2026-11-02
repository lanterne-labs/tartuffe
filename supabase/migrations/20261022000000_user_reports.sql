-- +goose Up
-- A signed-in user's report that a fact or a contradiction is wrong. The
-- worker hands each pending report to the complaint judge (LLM) with the
-- target and its sources: accepted removes the target, rejected keeps it,
-- ambiguous waits for a person. A user reports a target once. The row keeps
-- the target's text and person as they were reported, so the public log of
-- corrections outlives the removed target; the user goes NULL when the
-- account is deleted, the report stays.
CREATE TABLE IF NOT EXISTS user_reports (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT REFERENCES app_users(id) ON DELETE SET NULL,
    target_kind TEXT NOT NULL CHECK (target_kind IN ('fact', 'contradiction')),
    target_id BIGINT NOT NULL,
    person_id BIGINT NOT NULL,
    person_name TEXT NOT NULL,
    target_text TEXT NOT NULL,
    reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 10 AND 2000),
    status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'accepted', 'rejected', 'ambiguous', 'stale')),
    verdict_reason TEXT NOT NULL DEFAULT '',
    attempts INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    judged_at TIMESTAMPTZ,
    UNIQUE (user_id, target_kind, target_id)
);
CREATE INDEX IF NOT EXISTS idx_user_reports_pending ON user_reports (created_at) WHERE status = 'pending';
CREATE INDEX IF NOT EXISTS idx_user_reports_accepted ON user_reports (judged_at DESC) WHERE status = 'accepted';
CREATE INDEX IF NOT EXISTS idx_user_reports_target ON user_reports (target_kind, target_id);

-- The feed lists the latest contradictions and facts.
CREATE INDEX IF NOT EXISTS idx_contradictions_detected_at ON contradictions (detected_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_facts_created_at ON facts (created_at DESC, id DESC);

-- +goose Down
DROP INDEX IF EXISTS idx_facts_created_at;
DROP INDEX IF EXISTS idx_contradictions_detected_at;
DROP TABLE IF EXISTS user_reports;
