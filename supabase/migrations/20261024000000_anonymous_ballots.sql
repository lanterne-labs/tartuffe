-- +goose Up
-- Anonymous ballots. A document (a passport or ID card, through ZKPassport)
-- admits the commitment of a secret kept on the voter's devices; a ballot
-- proves in zero knowledge that its author holds one of the admitted
-- commitments, without saying which (Semaphore). Nothing ties a ballot to an
-- account or a document any more: the account ballots go.
DROP TABLE IF EXISTS poll_choices;
DROP TABLE IF EXISTS poll_consents;

-- The public log: every registration, voter tree root, ballot and tally, in
-- order, each entry hashing the one before. body is the exact JSON hashed.
CREATE TABLE IF NOT EXISTS vote_log (
    seq BIGSERIAL PRIMARY KEY,
    kind TEXT NOT NULL,
    body TEXT NOT NULL,
    prev_hash BYTEA NOT NULL,
    hash BYTEA NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The voters' commitments. document is the HMAC of the provider's document
-- identifier. A commitment belongs to the windows from_a/from_b (included)
-- to until_a/until_b (excluded, NULL: open) of the two series of windows;
-- a re-registration closes the old commitment at the next window, where the
-- new one starts, so a document never holds two commitments in one window.
CREATE TABLE IF NOT EXISTS voter_keys (
    id BIGSERIAL PRIMARY KEY,
    document TEXT NOT NULL,
    provider TEXT NOT NULL,
    commitment TEXT NOT NULL UNIQUE,
    from_a INT NOT NULL,
    from_b INT NOT NULL,
    until_a INT,
    until_b INT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX IF NOT EXISTS voter_keys_one_open ON voter_keys (document) WHERE until_a IS NULL;
CREATE INDEX IF NOT EXISTS voter_keys_document ON voter_keys (document);

-- The roots each window's tree had: a ballot proves against one of them.
CREATE TABLE IF NOT EXISTS voter_roots (
    series SMALLINT NOT NULL,
    win INT NOT NULL,
    root TEXT NOT NULL,
    size INT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (series, win, root)
);

-- The ballots. One nullifier is one voter in one window of one series of
-- one poll; its ballot with the highest seq counts. A ballot waits
-- (published_at NULL) until the next batch publishes it in the log, shuffled
-- with the others. kind: choices, blank or withdraw.
CREATE TABLE IF NOT EXISTS anon_ballots (
    event_id BIGINT NOT NULL REFERENCES poll_events(id) ON DELETE CASCADE,
    series SMALLINT NOT NULL,
    win INT NOT NULL,
    nullifier TEXT NOT NULL,
    seq INT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('choices', 'blank', 'withdraw')),
    choices BIGINT[] NOT NULL DEFAULT '{}',
    root TEXT NOT NULL,
    proof JSONB NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ,
    log_seq BIGINT,
    PRIMARY KEY (event_id, series, win, nullifier, seq)
);
CREATE INDEX IF NOT EXISTS anon_ballots_pending ON anon_ballots (received_at) WHERE published_at IS NULL;

-- The voting-key vault: the voter's secret, encrypted on the device with a
-- key only their passkey gives, under a lookup only that passkey gives too.
-- write_hash is the hash of a token the same passkey gives, so only it can
-- replace the entry.
CREATE TABLE IF NOT EXISTS key_vault (
    lookup TEXT PRIMARY KEY,
    ciphertext TEXT NOT NULL,
    write_hash TEXT NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Device links: the new device shows a code, the old one leaves here the
-- voting key and a sign-in token, encrypted for the new device's key.
CREATE TABLE IF NOT EXISTS device_links (
    channel TEXT PRIMARY KEY,
    ciphertext TEXT,
    expires_at TIMESTAMPTZ NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS device_links;
DROP TABLE IF EXISTS key_vault;
DROP TABLE IF EXISTS anon_ballots;
DROP TABLE IF EXISTS voter_roots;
DROP TABLE IF EXISTS voter_keys;
DROP TABLE IF EXISTS vote_log;
CREATE TABLE IF NOT EXISTS poll_consents (
    user_id BIGINT PRIMARY KEY REFERENCES app_users(id) ON DELETE CASCADE,
    consent_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS poll_choices (
    event_id BIGINT NOT NULL REFERENCES poll_events(id) ON DELETE CASCADE,
    user_id BIGINT NOT NULL REFERENCES app_users(id) ON DELETE CASCADE,
    rank INT NOT NULL,
    person_id BIGINT NOT NULL,
    PRIMARY KEY (event_id, user_id, rank)
);
