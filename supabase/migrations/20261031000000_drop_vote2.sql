-- +goose Up
-- tartuffe-vote/2 is replaced by isoloir (github.com/tartuffeme/isoloir),
-- which keeps its own tables (isoloir_*): the Go vote's tables go, with
-- its public log.
DROP TABLE IF EXISTS vote2_pending;
DROP TABLE IF EXISTS vote2_cursors;
DROP TABLE IF EXISTS vote2_checkpoints;
DROP TABLE IF EXISTS vote2_ballots;
DROP TABLE IF EXISTS vote2_registrations;
DROP TABLE IF EXISTS vote2_events;
DROP TABLE IF EXISTS vote_log_stamps;
DROP TABLE IF EXISTS vote_log_hashes;
DROP TABLE IF EXISTS vote_log;
DROP TABLE IF EXISTS poll_choices;

-- +goose Down
-- The tables of tartuffe-vote/2 are not recreated.
SELECT 1;
