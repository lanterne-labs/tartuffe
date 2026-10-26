package vote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"digital-mirror/backend/polls"
	"digital-mirror/backend/vote/ballot"
)

// Store is what the ingest keeps beside the log (migration
// 20261027000000_vote2.sql), over the polls' store of the log itself.
type Store struct {
	pool *pgxpool.Pool
	Log  *polls.PGStore
}

// NewStore returns the store on a pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, Log: polls.NewPGStore(pool)}
}

// Setups returns the setups of an event in log order, with their seqs
// and the time they were logged.
func (s *Store) Setups(ctx context.Context, eventID int64) ([]LoggedSetup, error) {
	rows, err := s.pool.Query(ctx, `SELECT seq, body, created_at FROM vote_log WHERE kind = $1 ORDER BY seq`, KindSetup)
	if err != nil {
		return nil, fmt.Errorf("setups: %w", err)
	}
	defer rows.Close()
	var out []LoggedSetup
	for rows.Next() {
		var ls LoggedSetup
		var body string
		if err := rows.Scan(&ls.Seq, &body, &ls.LoggedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(body), &ls.Setup); err != nil {
			return nil, fmt.Errorf("setup %d: %w", ls.Seq, err)
		}
		if ls.Setup.Event == eventID {
			out = append(out, ls)
		}
	}
	return out, rows.Err()
}

// LoggedSetup is a setup entry with its place in the log.
type LoggedSetup struct {
	Seq      int64
	LoggedAt time.Time
	Setup    Setup
}

// Registration is a row of vote2_registrations.
type Registration struct {
	EventID   int64
	UID       string
	ID        string
	Position  int
	LogSeq    int64
	ProofDate time.Time
	NostrID   string
	Relay     string
}

// HasUID and HasID tell whether a passport or a leaf registered already.
func (s *Store) HasUID(ctx context.Context, eventID int64, uid string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vote2_registrations WHERE event_id = $1 AND uid = $2)`, eventID, uid).Scan(&ok)
	return ok, err
}

func (s *Store) HasID(ctx context.Context, eventID int64, id string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vote2_registrations WHERE event_id = $1 AND id = $2)`, eventID, id).Scan(&ok)
	return ok, err
}

// AddRegistration inserts a registration.
func (s *Store) AddRegistration(ctx context.Context, r Registration) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO vote2_registrations (event_id, uid, id, position, log_seq, proof_date, nostr_id, relay)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, r.EventID, r.UID, r.ID, r.Position, r.LogSeq, r.ProofDate, r.NostrID, r.Relay)
	if err != nil {
		return fmt.Errorf("add registration: %w", err)
	}
	return nil
}

// Leaves returns the leaves of an event's tree in position order.
func (s *Store) Leaves(ctx context.Context, eventID int64) ([]fr.Element, error) {
	rows, err := s.pool.Query(ctx, `SELECT position, id FROM vote2_registrations WHERE event_id = $1 ORDER BY position`, eventID)
	if err != nil {
		return nil, fmt.Errorf("leaves: %w", err)
	}
	defer rows.Close()
	var out []fr.Element
	for rows.Next() {
		var pos int
		var id string
		if err := rows.Scan(&pos, &id); err != nil {
			return nil, err
		}
		if pos != len(out) {
			return nil, fmt.Errorf("leaves: position %d where %d was expected", pos, len(out))
		}
		e, err := parseField(id)
		if err != nil {
			return nil, fmt.Errorf("leaf %d: %w", pos, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestSeq returns the highest logged seq of a pseudonym, 0 for none.
func (s *Store) LatestSeq(ctx context.Context, eventID int64, nullifier string) (uint32, error) {
	var seq *int32
	err := s.pool.QueryRow(ctx, `SELECT MAX(seq) FROM vote2_ballots WHERE event_id = $1 AND nullifier = $2`, eventID, nullifier).Scan(&seq)
	if err != nil || seq == nil {
		return 0, err
	}
	return uint32(*seq), nil
}

// HasPIHash tells whether a ballot with these public inputs was logged.
func (s *Store) HasPIHash(ctx context.Context, pi string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vote2_ballots WHERE pi_hash = $1)`, pi).Scan(&ok)
	return ok, err
}

// LoggedBallot is a row of vote2_ballots.
type LoggedBallot struct {
	EventID    int64
	Nullifier  string
	Seq        uint32
	PIHash     string
	LogSeq     int64
	Checkpoint int64
	Body       []byte
	NostrID    string
	Relay      string
}

// AddBallot inserts a logged ballot.
func (s *Store) AddBallot(ctx context.Context, b LoggedBallot) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO vote2_ballots (event_id, nullifier, seq, pi_hash, log_seq, checkpoint, body, nostr_id, relay)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, b.EventID, b.Nullifier, b.Seq, b.PIHash, b.LogSeq, b.Checkpoint, b.Body, b.NostrID, b.Relay)
	if err != nil {
		return fmt.Errorf("add ballot: %w", err)
	}
	return nil
}

// HandledEvent is a row of vote2_events.
type HandledEvent struct {
	NostrID    string
	Relay      string
	Kind       int
	BodyHash   string
	PIHash     string
	Outcome    string
	Reason     string
	LogSeq     int64
	Checkpoint int64
}

// Handled tells whether a Nostr event was handled, and how.
func (s *Store) Handled(ctx context.Context, nostrID string) (*HandledEvent, error) {
	var h HandledEvent
	var pi, reason *string
	var seq *int64
	err := s.pool.QueryRow(ctx, `SELECT nostr_id, relay, kind, body_hash, pi_hash, outcome, reason, log_seq, checkpoint FROM vote2_events WHERE nostr_id = $1`, nostrID).
		Scan(&h.NostrID, &h.Relay, &h.Kind, &h.BodyHash, &pi, &h.Outcome, &reason, &seq, &h.Checkpoint)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("handled: %w", err)
	}
	if pi != nil {
		h.PIHash = *pi
	}
	if reason != nil {
		h.Reason = *reason
	}
	if seq != nil {
		h.LogSeq = *seq
	}
	return &h, nil
}

// AddHandled records how an event was handled.
func (s *Store) AddHandled(ctx context.Context, h HandledEvent) error {
	var pi, reason *string
	var seq *int64
	if h.PIHash != "" {
		pi = &h.PIHash
	}
	if h.Reason != "" {
		reason = &h.Reason
	}
	if h.LogSeq != 0 {
		seq = &h.LogSeq
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO vote2_events (nostr_id, relay, kind, body_hash, pi_hash, outcome, reason, log_seq, checkpoint)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9) ON CONFLICT (nostr_id) DO NOTHING`,
		h.NostrID, h.Relay, h.Kind, h.BodyHash, pi, h.Outcome, reason, seq, h.Checkpoint)
	if err != nil {
		return fmt.Errorf("add handled: %w", err)
	}
	return nil
}

// Checkpoint is a row of vote2_checkpoints.
type Checkpoint struct {
	Index     int64
	LogSize   int64
	TreeRoot  string
	TreeSize  int
	Commit    string
	Note      string
	CreatedAt time.Time
}

// LastCheckpoint returns the latest checkpoint, nil before the first.
func (s *Store) LastCheckpoint(ctx context.Context) (*Checkpoint, error) {
	var c Checkpoint
	err := s.pool.QueryRow(ctx, `SELECT idx, log_size, tree_root, tree_size, mirror_commit, note, created_at FROM vote2_checkpoints ORDER BY idx DESC LIMIT 1`).
		Scan(&c.Index, &c.LogSize, &c.TreeRoot, &c.TreeSize, &c.Commit, &c.Note, &c.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("last checkpoint: %w", err)
	}
	return &c, nil
}

// AddCheckpoint records a checkpoint.
func (s *Store) AddCheckpoint(ctx context.Context, c Checkpoint) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO vote2_checkpoints (idx, log_size, tree_root, tree_size, mirror_commit, note) VALUES ($1, $2, $3, $4, $5, $6)`,
		c.Index, c.LogSize, c.TreeRoot, c.TreeSize, c.Commit, c.Note)
	if err != nil {
		return fmt.Errorf("add checkpoint: %w", err)
	}
	return nil
}

// RootAge returns when a tree root was first in a checkpoint, or false.
func (s *Store) RootAge(ctx context.Context, root string) (time.Time, bool, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx, `SELECT created_at FROM vote2_checkpoints WHERE tree_root = $1 ORDER BY idx LIMIT 1`, root).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return at, false, nil
	}
	if err != nil {
		return at, false, fmt.Errorf("root age: %w", err)
	}
	return at, true, nil
}

// Cursor and SetCursor keep where the reading of a relay stopped.
func (s *Store) Cursor(ctx context.Context, relay string) (int64, error) {
	var since int64
	err := s.pool.QueryRow(ctx, `SELECT since FROM vote2_cursors WHERE relay = $1`, relay).Scan(&since)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return since, err
}

func (s *Store) SetCursor(ctx context.Context, relay string, since int64) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO vote2_cursors (relay, since) VALUES ($1, $2) ON CONFLICT (relay) DO UPDATE SET since = GREATEST(vote2_cursors.since, EXCLUDED.since)`, relay, since)
	return err
}

// LoadTree rebuilds an event's tree from its registrations.
func (s *Store) LoadTree(ctx context.Context, eventID int64) (*ballot.Tree, error) {
	leaves, err := s.Leaves(ctx, eventID)
	if err != nil {
		return nil, err
	}
	t := ballot.NewTree()
	for _, l := range leaves {
		if _, err := t.Append(l); err != nil {
			return nil, err
		}
	}
	return t, nil
}
