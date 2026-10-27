package vote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bn254/fr"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"digital-mirror/backend/polls"
	"digital-mirror/backend/vote/ballot"
)

// Store is what the ingest keeps beside the log (migration
// 20261027000000_vote2.sql), over the polls' store of the log itself.
type Store struct {
	pool *pgxpool.Pool
	db   querier // the pool, or the transaction of Tx
	tx   pgx.Tx  // set inside Tx
	Log  *polls.PGStore
}

// querier is what the pool and a transaction share.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// NewStore returns the store on a pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, db: pool, Log: polls.NewPGStore(pool)}
}

// Tx runs fn with a store whose writes are one transaction, committed when
// fn returns nil. The checkpoint logs its entries, their index rows and
// the pending rows' deletion in one: a failure leaves nothing half done.
func (s *Store) Tx(ctx context.Context, fn func(ctx context.Context, tx *Store) error) error {
	t, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer t.Rollback(ctx)
	if err := fn(ctx, &Store{pool: s.pool, db: t, tx: t, Log: s.Log}); err != nil {
		return err
	}
	if err := t.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// AppendLog appends a log entry: in the transaction inside Tx, on its own
// outside.
func (s *Store) AppendLog(ctx context.Context, kind string, body json.RawMessage) (int64, error) {
	if s.tx != nil {
		return s.Log.AppendIn(ctx, s.tx, kind, body)
	}
	return s.Log.Append(ctx, kind, body)
}

// Lease keys of the writers (pg_advisory_lock): one ingest, one tallier.
const (
	leaseIngest  = 0x7461727475666632 // "tartuff2"
	leaseTallier = 0x7461727475666633
)

// Lease is a writer's hold on the log for the life of its process.
type Lease struct{ conn *pgxpool.Conn }

// Release gives the lease back.
func (l *Lease) Release(ctx context.Context) {
	if l == nil || l.conn == nil {
		return
	}
	_, _ = l.conn.Exec(ctx, `SELECT pg_advisory_unlock_all()`)
	l.conn.Release()
	l.conn = nil
}

// Acquire takes a writer's lease on a connection kept for the lease's life,
// or fails at once when another process holds it: two ingests would
// acknowledge the same checkpoint index and log each other's entries twice.
func (s *Store) Acquire(ctx context.Context, key int64, who string) (*Lease, error) {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("lease: %w", err)
	}
	var ok bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&ok); err != nil {
		conn.Release()
		return nil, fmt.Errorf("lease: %w", err)
	}
	if !ok {
		conn.Release()
		return nil, fmt.Errorf("another %s holds the log: one at a time", who)
	}
	return &Lease{conn: conn}, nil
}

// PendingRow is a row of vote2_pending: an entry acknowledged for the
// checkpoint named, with the message it came from.
type PendingRow struct {
	Msg        Message
	Kind       string // the log entry's kind
	Body       []byte // canonical
	Hash       string
	Nullifier  string
	Seq        uint32
	UID, ID    string
	ProofDate  time.Time
	Reason     string
	Checkpoint int64
}

// AddPending records an entry before its acknowledgement is posted.
func (s *Store) AddPending(ctx context.Context, p PendingRow) error {
	var pd *time.Time
	if !p.ProofDate.IsZero() {
		pd = &p.ProofDate
	}
	_, err := s.db.Exec(ctx, `INSERT INTO vote2_pending (nostr_id, relay, msg_kind, msg_body, msg_time, kind, body, hash, nullifier, seq, uid, id, proof_date, reason, checkpoint)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		p.Msg.ID, p.Msg.Box, int(p.Msg.Kind), p.Msg.Body, p.Msg.Time, p.Kind, p.Body, p.Hash, p.Nullifier, int64(p.Seq), p.UID, p.ID, pd, p.Reason, p.Checkpoint)
	if err != nil {
		return fmt.Errorf("add pending: %w", err)
	}
	return nil
}

// Pending lists the entries acknowledged and not yet logged, oldest first.
func (s *Store) Pending(ctx context.Context) ([]PendingRow, error) {
	rows, err := s.db.Query(ctx, `SELECT nostr_id, relay, msg_kind, msg_body, msg_time, kind, body, hash, nullifier, seq, uid, id, proof_date, reason, checkpoint FROM vote2_pending ORDER BY received_at, nostr_id`)
	if err != nil {
		return nil, fmt.Errorf("pending: %w", err)
	}
	defer rows.Close()
	var out []PendingRow
	for rows.Next() {
		var p PendingRow
		var kind int
		var seq int64
		var pd *time.Time
		if err := rows.Scan(&p.Msg.ID, &p.Msg.Box, &kind, &p.Msg.Body, &p.Msg.Time, &p.Kind, &p.Body, &p.Hash, &p.Nullifier, &seq, &p.UID, &p.ID, &pd, &p.Reason, &p.Checkpoint); err != nil {
			return nil, err
		}
		p.Msg.Kind = MessageKind(kind)
		p.Seq = uint32(seq)
		if pd != nil {
			p.ProofDate = *pd
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePending drops a logged entry's pending row.
func (s *Store) DeletePending(ctx context.Context, nostrID string) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM vote2_pending WHERE nostr_id = $1`, nostrID); err != nil {
		return fmt.Errorf("delete pending: %w", err)
	}
	return nil
}

// Setups returns the setups of an event in log order, with their seqs
// and the time they were logged.
func (s *Store) Setups(ctx context.Context, eventID int64) ([]LoggedSetup, error) {
	rows, err := s.db.Query(ctx, `SELECT seq, body, created_at FROM vote_log WHERE kind = $1 ORDER BY seq`, KindSetup)
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
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vote2_registrations WHERE event_id = $1 AND uid = $2)`, eventID, uid).Scan(&ok)
	return ok, err
}

func (s *Store) HasID(ctx context.Context, eventID int64, id string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vote2_registrations WHERE event_id = $1 AND id = $2)`, eventID, id).Scan(&ok)
	return ok, err
}

// AddRegistration inserts a registration.
func (s *Store) AddRegistration(ctx context.Context, r Registration) error {
	_, err := s.db.Exec(ctx, `INSERT INTO vote2_registrations (event_id, uid, id, position, log_seq, proof_date, nostr_id, relay)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, r.EventID, r.UID, r.ID, r.Position, r.LogSeq, r.ProofDate, r.NostrID, r.Relay)
	if err != nil {
		return fmt.Errorf("add registration: %w", err)
	}
	return nil
}

// Leaves returns the leaves of an event's tree in position order.
func (s *Store) Leaves(ctx context.Context, eventID int64) ([]fr.Element, error) {
	rows, err := s.db.Query(ctx, `SELECT position, id FROM vote2_registrations WHERE event_id = $1 ORDER BY position`, eventID)
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
	var seq *int64
	err := s.db.QueryRow(ctx, `SELECT MAX(seq) FROM vote2_ballots WHERE event_id = $1 AND nullifier = $2`, eventID, nullifier).Scan(&seq)
	if err != nil || seq == nil {
		return 0, err
	}
	return uint32(*seq), nil
}

// HasPIHash tells whether a ballot with these public inputs was logged.
func (s *Store) HasPIHash(ctx context.Context, pi string) (bool, error) {
	var ok bool
	err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM vote2_ballots WHERE pi_hash = $1)`, pi).Scan(&ok)
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
	_, err := s.db.Exec(ctx, `INSERT INTO vote2_ballots (event_id, nullifier, seq, pi_hash, log_seq, checkpoint, body, nostr_id, relay)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`, b.EventID, b.Nullifier, int64(b.Seq), b.PIHash, b.LogSeq, b.Checkpoint, b.Body, b.NostrID, b.Relay)
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
	err := s.db.QueryRow(ctx, `SELECT nostr_id, relay, kind, body_hash, pi_hash, outcome, reason, log_seq, checkpoint FROM vote2_events WHERE nostr_id = $1`, nostrID).
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
	_, err := s.db.Exec(ctx, `INSERT INTO vote2_events (nostr_id, relay, kind, body_hash, pi_hash, outcome, reason, log_seq, checkpoint)
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
	err := s.db.QueryRow(ctx, `SELECT idx, log_size, tree_root, tree_size, mirror_commit, note, created_at FROM vote2_checkpoints ORDER BY idx DESC LIMIT 1`).
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
	_, err := s.db.Exec(ctx, `INSERT INTO vote2_checkpoints (idx, log_size, tree_root, tree_size, mirror_commit, note) VALUES ($1, $2, $3, $4, $5, $6)`,
		c.Index, c.LogSize, c.TreeRoot, c.TreeSize, c.Commit, c.Note)
	if err != nil {
		return fmt.Errorf("add checkpoint: %w", err)
	}
	return nil
}

// RootAge returns when a tree root was first in a checkpoint, or false.
func (s *Store) RootAge(ctx context.Context, root string) (time.Time, bool, error) {
	var at time.Time
	err := s.db.QueryRow(ctx, `SELECT created_at FROM vote2_checkpoints WHERE tree_root = $1 ORDER BY idx LIMIT 1`, root).Scan(&at)
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
	err := s.db.QueryRow(ctx, `SELECT since FROM vote2_cursors WHERE relay = $1`, relay).Scan(&since)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	return since, err
}

func (s *Store) SetCursor(ctx context.Context, relay string, since int64) error {
	_, err := s.db.Exec(ctx, `INSERT INTO vote2_cursors (relay, since) VALUES ($1, $2) ON CONFLICT (relay) DO UPDATE SET since = GREATEST(vote2_cursors.since, EXCLUDED.since)`, relay, since)
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

// LeavesBetween returns the leaves logged with after < log_seq ≤ upto.
func (s *Store) LeavesBetween(ctx context.Context, eventID, after, upto int64) ([]Registration, error) {
	rows, err := s.db.Query(ctx, `SELECT position, id, log_seq FROM vote2_registrations WHERE event_id = $1 AND log_seq > $2 AND log_seq <= $3 ORDER BY position`, eventID, after, upto)
	if err != nil {
		return nil, fmt.Errorf("leaves between: %w", err)
	}
	defer rows.Close()
	var out []Registration
	for rows.Next() {
		var r Registration
		if err := rows.Scan(&r.Position, &r.ID, &r.LogSeq); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// LeavesRange returns the leaf ids at positions [from, to).
func (s *Store) LeavesRange(ctx context.Context, eventID int64, from, to int) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT id FROM vote2_registrations WHERE event_id = $1 AND position >= $2 AND position < $3 ORDER BY position`, eventID, from, to)
	if err != nil {
		return nil, fmt.Errorf("leaves range: %w", err)
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// PositionsShard returns id → position for the leaves whose id starts
// with the hex nibble.
func (s *Store) PositionsShard(ctx context.Context, eventID int64, nibble string) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `SELECT id, position FROM vote2_registrations WHERE event_id = $1 AND substr(id, 3, 1) = $2`, eventID, nibble)
	if err != nil {
		return nil, fmt.Errorf("positions shard: %w", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var id string
		var pos int
		if err := rows.Scan(&id, &pos); err != nil {
			return nil, err
		}
		out[id] = pos
	}
	return out, rows.Err()
}

// EntriesOf lists the entries of a checkpoint for the mirror.
func (s *Store) EntriesOf(ctx context.Context, checkpoint int64) ([]MirrorEntry, error) {
	rows, err := s.db.Query(ctx, `SELECT kind, body_hash, pi_hash, outcome, log_seq FROM vote2_events WHERE checkpoint = $1 ORDER BY log_seq`, checkpoint)
	if err != nil {
		return nil, fmt.Errorf("entries of: %w", err)
	}
	defer rows.Close()
	var out []MirrorEntry
	for rows.Next() {
		var kind int
		var bodyHash, outcome string
		var pi *string
		var seq *int64
		if err := rows.Scan(&kind, &bodyHash, &pi, &outcome, &seq); err != nil {
			return nil, err
		}
		e := MirrorEntry{Checkpoint: checkpoint, Hash: bodyHash}
		switch {
		case outcome == "rejected":
			e.Kind = KindReject
		case MessageKind(kind) == MsgBallot:
			e.Kind = KindBallot
			if pi != nil {
				e.Hash = *pi
			}
		case MessageKind(kind) == MsgRegister:
			e.Kind = KindRegister
		default:
			e.Kind = KindSeen
		}
		if seq != nil {
			e.LogSeq = *seq
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// NullifiersOf lists the pseudonyms with a ballot in a checkpoint.
func (s *Store) NullifiersOf(ctx context.Context, checkpoint int64) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT DISTINCT nullifier FROM vote2_ballots WHERE checkpoint = $1`, checkpoint)
	if err != nil {
		return nil, fmt.Errorf("nullifiers of: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// IndexShard returns nullifier → highest seq for the pseudonyms whose
// hex starts with the byte given.
func (s *Store) IndexShard(ctx context.Context, eventID int64, prefix string) (map[string]uint32, error) {
	rows, err := s.db.Query(ctx, `SELECT nullifier, MAX(seq) FROM vote2_ballots WHERE event_id = $1 AND substr(nullifier, 3, 2) = $2 GROUP BY nullifier`, eventID, prefix)
	if err != nil {
		return nil, fmt.Errorf("index shard: %w", err)
	}
	defer rows.Close()
	out := map[string]uint32{}
	for rows.Next() {
		var n string
		var seq int64
		if err := rows.Scan(&n, &seq); err != nil {
			return nil, err
		}
		out[n] = uint32(seq)
	}
	return out, rows.Err()
}

// Stats are the public counters of the mirror: ballots logged, ballots
// since the last tally, registrations. Never anything decrypted.
type Stats struct {
	Ballots       int64 `json:"ballots"`
	SinceTally    int64 `json:"since_tally"`
	Registrations int64 `json:"registrations"`
}

// Stats counts for the mirror.
func (s *Store) Stats(ctx context.Context, eventID int64) (Stats, error) {
	var st Stats
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM vote2_ballots WHERE event_id = $1`, eventID).Scan(&st.Ballots); err != nil {
		return st, fmt.Errorf("stats: %w", err)
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM vote2_registrations WHERE event_id = $1`, eventID).Scan(&st.Registrations); err != nil {
		return st, fmt.Errorf("stats: %w", err)
	}
	var upto int64
	if lt, err := s.LastTally(ctx, eventID); err == nil && lt != nil {
		upto = lt.Upto
	}
	if err := s.db.QueryRow(ctx, `SELECT COUNT(*) FROM vote2_ballots WHERE event_id = $1 AND log_seq > $2`, eventID, upto).Scan(&st.SinceTally); err != nil {
		return st, fmt.Errorf("stats: %w", err)
	}
	return st, nil
}

// IDOfUID returns the leaf a passport registered, "" for none.
func (s *Store) IDOfUID(ctx context.Context, eventID int64, uid string) (string, error) {
	var id string
	err := s.db.QueryRow(ctx, `SELECT id FROM vote2_registrations WHERE event_id = $1 AND uid = $2`, eventID, uid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

// Reset deletes everything of the vote: the protocol's tables and the
// public log with its hashes and stamps. Before a launch only.
func (s *Store) Reset(ctx context.Context) error {
	for _, q := range []string{
		`TRUNCATE vote2_events, vote2_ballots, vote2_registrations, vote2_checkpoints, vote2_cursors`,
		`TRUNCATE vote_log, vote_log_hashes, vote_log_stamps`,
	} {
		if _, err := s.db.Exec(ctx, q); err != nil {
			return fmt.Errorf("reset: %w", err)
		}
	}
	return nil
}
