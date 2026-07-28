-- +goose Up
-- Verdict pinning for the contradiction auditor: once a row is audited KEEP,
-- the hash of everything that determined that verdict (audit prompt + rulebook
-- + fact texts + dates + detection reason) is stored here. The audit step skips
-- rows whose current hash matches — the verdict is a pure function of its
-- inputs and cannot flip between runs. A prompt/rulebook change or a fact
-- repoint changes the hash and naturally re-opens the row for auditing.
ALTER TABLE contradictions ADD COLUMN IF NOT EXISTS last_audit_hash TEXT;

-- +goose Down
ALTER TABLE contradictions DROP COLUMN IF EXISTS last_audit_hash;
