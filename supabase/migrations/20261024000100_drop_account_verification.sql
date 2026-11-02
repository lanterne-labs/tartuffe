-- +goose Up
-- A document no longer attaches to an account: it admits an anonymous voting
-- key (voter_keys) and the account keeps nothing of it. The document of each
-- account and the moves of a document between accounts go.
DROP TABLE IF EXISTS identity_moves;
DROP INDEX IF EXISTS app_users_verified_subject_key;
ALTER TABLE app_users DROP COLUMN IF EXISTS verified_subject, DROP COLUMN IF EXISTS verified_at;

-- +goose Down
ALTER TABLE app_users ADD COLUMN IF NOT EXISTS verified_subject TEXT;
ALTER TABLE app_users ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ;
CREATE UNIQUE INDEX IF NOT EXISTS app_users_verified_subject_key ON app_users (verified_subject);
CREATE TABLE IF NOT EXISTS identity_moves (
    token_hash BYTEA PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    subject TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);
