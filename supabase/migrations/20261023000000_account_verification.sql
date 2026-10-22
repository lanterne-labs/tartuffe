-- +goose Up
-- An account comes from an account provider (Google, noop); a verified
-- identity (a passport proved with ZKPassport today) is attached to it
-- afterwards and gives it the right to vote in every poll. verified_subject
-- is "<provider>:<identifier of the document for this site>": one document,
-- one account. A document shown on another account moves there with its
-- ballots (storage.AttachVerification).
ALTER TABLE app_users ADD COLUMN IF NOT EXISTS verified_subject TEXT;
ALTER TABLE app_users ADD COLUMN IF NOT EXISTS verified_at TIMESTAMPTZ;
CREATE UNIQUE INDEX IF NOT EXISTS app_users_verified_subject_key ON app_users (verified_subject);

-- The accounts signed in with ZKPassport before were proved by their document.
UPDATE app_users SET verified_subject = 'zkpassport:' || subject, verified_at = created_at
WHERE provider = 'zkpassport' AND verified_subject IS NULL;

-- +goose Down
DROP INDEX IF EXISTS app_users_verified_subject_key;
ALTER TABLE app_users DROP COLUMN IF EXISTS verified_at;
ALTER TABLE app_users DROP COLUMN IF EXISTS verified_subject;
