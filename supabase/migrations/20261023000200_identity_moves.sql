-- +goose Up
-- A verified identity shown on a second account does not move at once: the
-- user is asked first (the old account loses its votes to this one). The
-- proof is checked once; while the user answers, the move waits here, by
-- the hash of a one-time token given to the page (10 minutes).
CREATE TABLE IF NOT EXISTS identity_moves (
    token_hash BYTEA PRIMARY KEY,
    user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    subject TEXT NOT NULL,
    expires_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS identity_moves;
