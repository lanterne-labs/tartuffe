-- +goose Up
-- What the audit skill (.claude/skills/audit) carries from one run to the
-- next: its cursor in the history and the numbers of its last run. In the
-- database, so that a run from another machine goes on where the last one
-- stopped.
CREATE TABLE IF NOT EXISTS audit_state (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS audit_state;
