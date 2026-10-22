-- +goose Up
-- The consent to store one's ballots belongs to the polls, not to the
-- account: the accounts package never reads it, the polls package owns this
-- table as it owns poll_choices.
CREATE TABLE IF NOT EXISTS poll_consents (
    user_id BIGINT PRIMARY KEY REFERENCES app_users(id) ON DELETE CASCADE,
    consent_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO poll_consents (user_id, consent_at)
SELECT id, consent_at FROM app_users WHERE consent_at IS NOT NULL
ON CONFLICT (user_id) DO NOTHING;
ALTER TABLE app_users DROP COLUMN IF EXISTS consent_at;

-- +goose Down
ALTER TABLE app_users ADD COLUMN IF NOT EXISTS consent_at TIMESTAMPTZ;
UPDATE app_users u SET consent_at = c.consent_at FROM poll_consents c WHERE c.user_id = u.id;
DROP TABLE IF EXISTS poll_consents;
