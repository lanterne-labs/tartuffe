-- +goose Up

-- judge_hash records which judge rulebook (detector prompt + refutation prompt
-- + vote count) produced a pair verdict. The nightly pipeline re-opens negative
-- verdicts whose hash is not the current one — oldest first, budgeted — so a
-- prompt change propagates to the whole backlog of NOs, the way the auditor's
-- last_audit_hash already propagates it to the YESes. NULL = judged before the
-- column existed: every such NO is re-judged once.
ALTER TABLE fact_pair_checks ADD COLUMN IF NOT EXISTS judge_hash TEXT;

CREATE INDEX IF NOT EXISTS idx_fpc_negative_judge_hash
    ON fact_pair_checks (judge_hash, checked_at)
    WHERE is_contradiction = false;

-- +goose Down
DROP INDEX IF EXISTS idx_fpc_negative_judge_hash;
ALTER TABLE fact_pair_checks DROP COLUMN IF EXISTS judge_hash;
