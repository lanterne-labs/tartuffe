-- +goose Up
-- tartuffe-vote/1 (plain anonymous ballots in windows, Semaphore, the key
-- vault and device links through our server) is replaced by tartuffe-vote/2
-- (docs/vote/SPEC.md): its tables go. The public log stays.
DROP TABLE IF EXISTS anon_ballots;
DROP TABLE IF EXISTS voter_roots;
DROP TABLE IF EXISTS voter_keys;
DROP TABLE IF EXISTS key_vault;
DROP TABLE IF EXISTS device_links;
DROP TABLE IF EXISTS nostr_cursors;
DROP TABLE IF EXISTS poll_consents;
DROP TABLE IF EXISTS identity_moves;

-- +goose Down
-- The tables of tartuffe-vote/1 are not recreated.
SELECT 1;
