-- +goose Up
-- tartuffe-vote/2 (docs/vote/SPEC.md): what the ingest server keeps beside
-- the public log. The log (vote_log, vote_log_hashes, vote_log_stamps)
-- stays the record; these tables index it.

-- The registrations of an event: one per passport identifier (uid) and one
-- per leaf (id), with the leaf's position in the voter tree.
CREATE TABLE IF NOT EXISTS vote2_registrations (
    event_id BIGINT NOT NULL REFERENCES poll_events(id) ON DELETE CASCADE,
    uid TEXT NOT NULL,
    id TEXT NOT NULL,
    position INTEGER NOT NULL,
    log_seq BIGINT NOT NULL,
    proof_date TIMESTAMPTZ NOT NULL,
    nostr_id TEXT NOT NULL,
    relay TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, position),
    UNIQUE (event_id, uid),
    UNIQUE (event_id, id)
);

-- The ballots logged: the latest seq per pseudonym counts.
CREATE TABLE IF NOT EXISTS vote2_ballots (
    event_id BIGINT NOT NULL REFERENCES poll_events(id) ON DELETE CASCADE,
    nullifier TEXT NOT NULL,
    seq INTEGER NOT NULL,
    pi_hash TEXT NOT NULL,
    log_seq BIGINT NOT NULL,
    checkpoint BIGINT NOT NULL,
    body JSONB NOT NULL,
    nostr_id TEXT NOT NULL,
    relay TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (event_id, nullifier, seq)
);
CREATE INDEX IF NOT EXISTS vote2_ballots_log_seq ON vote2_ballots (log_seq);
CREATE INDEX IF NOT EXISTS vote2_ballots_pi_hash ON vote2_ballots (pi_hash);

-- Every protocol event handled, by Nostr id: what the acknowledgement said.
CREATE TABLE IF NOT EXISTS vote2_events (
    nostr_id TEXT PRIMARY KEY,
    relay TEXT NOT NULL,
    kind INTEGER NOT NULL,
    body_hash TEXT NOT NULL,
    pi_hash TEXT,
    outcome TEXT NOT NULL,
    reason TEXT,
    log_seq BIGINT,
    checkpoint BIGINT NOT NULL,
    seen_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS vote2_events_checkpoint ON vote2_events (checkpoint);

-- The checkpoints: the signed note and what it carries.
CREATE TABLE IF NOT EXISTS vote2_checkpoints (
    idx BIGINT PRIMARY KEY,
    log_size BIGINT NOT NULL,
    tree_root TEXT NOT NULL,
    tree_size INTEGER NOT NULL,
    mirror_commit TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The relays' read cursors for the protocol's kinds.
CREATE TABLE IF NOT EXISTS vote2_cursors (
    relay TEXT PRIMARY KEY,
    since BIGINT NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS vote2_cursors;
DROP TABLE IF EXISTS vote2_checkpoints;
DROP TABLE IF EXISTS vote2_events;
DROP TABLE IF EXISTS vote2_ballots;
DROP TABLE IF EXISTS vote2_registrations;
