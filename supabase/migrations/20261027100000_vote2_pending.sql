-- +goose Up
-- The entries acknowledged and not yet logged (SPEC §7.2: the
-- acknowledgement is the obligation). The ingest writes the row before it
-- posts the acknowledgement and deletes it in the checkpoint's transaction,
-- so a restart logs what it promised at the checkpoint it named.
CREATE TABLE IF NOT EXISTS vote2_pending (
    nostr_id TEXT PRIMARY KEY,
    relay TEXT NOT NULL,
    msg_kind INTEGER NOT NULL,
    msg_body BYTEA NOT NULL,
    msg_time BIGINT NOT NULL,
    kind TEXT NOT NULL,
    body BYTEA NOT NULL,
    hash TEXT NOT NULL,
    nullifier TEXT NOT NULL DEFAULT '',
    seq BIGINT NOT NULL DEFAULT 0,
    uid TEXT NOT NULL DEFAULT '',
    id TEXT NOT NULL DEFAULT '',
    proof_date TIMESTAMPTZ,
    reason TEXT NOT NULL DEFAULT '',
    checkpoint BIGINT NOT NULL,
    received_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- A ballot's seq is a 32-bit unsigned value in the circuit: INTEGER refused
-- the upper half and one such ballot stopped every later checkpoint.
ALTER TABLE vote2_ballots ALTER COLUMN seq TYPE BIGINT;

-- +goose Down
ALTER TABLE vote2_ballots ALTER COLUMN seq TYPE INTEGER;
DROP TABLE IF EXISTS vote2_pending;
