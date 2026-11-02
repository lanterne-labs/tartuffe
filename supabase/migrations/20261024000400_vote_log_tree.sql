-- +goose Up
-- The public log becomes a Merkle tree (RFC 6962, Go's tlog): its stored
-- hashes replace the hash chain, signed checkpoints commit to it, and
-- OpenTimestamps stamps the checkpoints on Bitcoin. The API fills the tree
-- of the entries logged before at start.
ALTER TABLE vote_log DROP COLUMN IF EXISTS prev_hash;
ALTER TABLE vote_log DROP COLUMN IF EXISTS hash;
CREATE TABLE IF NOT EXISTS vote_log_hashes (
    idx BIGINT PRIMARY KEY,
    hash BYTEA NOT NULL
);
-- A checkpoint sent to the OpenTimestamps calendars: the signed note and the
-- .ots proof the calendars gave (pending until Bitcoin confirms it; anyone
-- upgrades it with the ots client).
CREATE TABLE IF NOT EXISTS vote_log_stamps (
    size BIGINT PRIMARY KEY,
    checkpoint TEXT NOT NULL,
    ots BYTEA NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- The ballots read from the Nostr relays: the last event seen of each relay,
-- to read on from there after a restart.
CREATE TABLE IF NOT EXISTS nostr_cursors (
    relay TEXT PRIMARY KEY,
    since BIGINT NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS nostr_cursors;
DROP TABLE IF EXISTS vote_log_stamps;
DROP TABLE IF EXISTS vote_log_hashes;
ALTER TABLE vote_log ADD COLUMN IF NOT EXISTS prev_hash BYTEA;
ALTER TABLE vote_log ADD COLUMN IF NOT EXISTS hash BYTEA;
