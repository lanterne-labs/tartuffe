-- +goose Up
-- A report is decided by a person, not by the AI. The judge (LLM) still reads
-- each report with its target and sources, but its verdict is only advice
-- (ai_verdict, verdict_reason): the report then waits for review, and the
-- target is removed only when a person accepts it (enabled true; false: the
-- person refused it). Statuses: pending (the AI has not read it), review (it
-- waits for a person), accepted, rejected, stale (the target was already
-- gone). The reports the AI accepted before this were applied already: they
-- stay accepted. The ones it rejected or found ambiguous go back to review.
ALTER TABLE user_reports
    ADD COLUMN IF NOT EXISTS ai_verdict TEXT CHECK (ai_verdict IN ('delete_target', 'dismiss_complaint', 'ambiguous')),
    ADD COLUMN IF NOT EXISTS enabled BOOLEAN,
    ADD COLUMN IF NOT EXISTS decided_at TIMESTAMPTZ;

ALTER TABLE user_reports DROP CONSTRAINT IF EXISTS user_reports_status_check;

UPDATE user_reports SET ai_verdict = 'delete_target', enabled = true, decided_at = judged_at WHERE status = 'accepted';
UPDATE user_reports SET ai_verdict = 'dismiss_complaint', status = 'review' WHERE status = 'rejected';
UPDATE user_reports SET ai_verdict = CASE WHEN verdict_reason LIKE 'judge failed:%' THEN NULL ELSE 'ambiguous' END, status = 'review'
WHERE status = 'ambiguous';

ALTER TABLE user_reports ADD CONSTRAINT user_reports_status_check
    CHECK (status IN ('pending', 'review', 'accepted', 'rejected', 'stale'));

CREATE INDEX IF NOT EXISTS idx_user_reports_review ON user_reports (judged_at) WHERE status = 'review';

-- +goose Down
DROP INDEX IF EXISTS idx_user_reports_review;
ALTER TABLE user_reports DROP CONSTRAINT IF EXISTS user_reports_status_check;
UPDATE user_reports SET status = 'ambiguous' WHERE status = 'review';
ALTER TABLE user_reports ADD CONSTRAINT user_reports_status_check
    CHECK (status IN ('pending', 'accepted', 'rejected', 'ambiguous', 'stale'));
ALTER TABLE user_reports DROP COLUMN IF EXISTS decided_at, DROP COLUMN IF EXISTS enabled, DROP COLUMN IF EXISTS ai_verdict;
