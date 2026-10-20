-- +goose Up
-- A one-time challenge of a sign-in: the browser asks for one, binds it into
-- the zero-knowledge proof (ZKPassport custom_data), and the sign-in consumes
-- it, so a proof seen once cannot sign in again.
CREATE TABLE IF NOT EXISTS auth_challenges (
    challenge TEXT PRIMARY KEY,
    provider TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_auth_challenges_expires ON auth_challenges (expires_at);

-- +goose Down
DROP TABLE IF EXISTS auth_challenges;
