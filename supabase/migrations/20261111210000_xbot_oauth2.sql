-- +goose Up
-- The X bot's OAuth 2.0 tokens (one row). X gives a new refresh token at
-- every renewal and ends the old one, so the current one cannot live in a
-- service variable: seed is the X_REFRESH_TOKEN it started from (a new value
-- of the variable starts again from it).
CREATE TABLE IF NOT EXISTS x_oauth (
    id BOOLEAN PRIMARY KEY DEFAULT true CHECK (id),
    seed TEXT NOT NULL,
    refresh_token TEXT NOT NULL,
    access_token TEXT NOT NULL DEFAULT '',
    expires_at TIMESTAMPTZ NOT NULL DEFAULT 'epoch',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- +goose Down
DROP TABLE IF EXISTS x_oauth;
