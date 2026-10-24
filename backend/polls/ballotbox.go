package polls

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Turnout counts the ballots of the results: Voters every ballot that
// counts, blank votes included, Blank those (the blank vote first). The
// blank vote's own count: BlankRanked the ballots that place it, BlankBeats
// how many rank it above each candidate.
type Turnout struct {
	Voters      int `json:"voters"`
	Blank       int `json:"blank"`
	BlankRanked int
	BlankBeats  map[int64]int
	// LogSize is the size of the public log the ballots were read with: the
	// count is that of its first LogSize entries, so anyone recounts the
	// results shown from them exactly.
	LogSize int64
	// Window is the results' window the ballots were counted in (nil: the
	// one at the moment the results are computed).
	Window *Window
}

// ErrUnknownRoot refuses a ballot proved against a root the window's tree
// never had.
var ErrUnknownRoot = fmt.Errorf("%w: the voter list it proves against is not this window's", ErrBallot)

// StaleSeqError refuses a ballot whose seq is not above the last one of its
// nullifier: Last is that one, for the browser to go on from.
type StaleSeqError struct{ Last int }

func (e *StaleSeqError) Error() string {
	return fmt.Sprintf("ballot refused: a newer ballot (seq %d) is already cast", e.Last)
}

func (e *StaleSeqError) Unwrap() error { return ErrBallot }

// Cast keeps a checked ballot (CheckAnonBallot) until the next batch
// publishes it: its root must be one of its window's, its seq above the last
// one of its nullifier.
func (s *PGStore) Cast(ctx context.Context, b AnonBallot) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("cast ballot: %w", err)
	}
	defer tx.Rollback(ctx)
	ok, err := knownRoot(ctx, tx, b.Window, b.Proof.MerkleTreeRoot)
	if err != nil {
		return err
	}
	if !ok {
		return ErrUnknownRoot
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, b.Proof.Nullifier); err != nil {
		return fmt.Errorf("lock nullifier: %w", err)
	}
	var last int
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM anon_ballots WHERE event_id = $1 AND series = $2 AND win = $3 AND nullifier = $4`,
		b.EventID, b.Series, b.Index, b.Proof.Nullifier).Scan(&last); err != nil {
		return fmt.Errorf("last ballot of nullifier: %w", err)
	}
	if b.Seq <= last {
		return &StaleSeqError{Last: last}
	}
	proof, err := json.Marshal(b.Proof)
	if err != nil {
		return fmt.Errorf("ballot proof: %w", err)
	}
	choices := b.Choices
	if choices == nil {
		choices = []int64{}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO anon_ballots (event_id, series, win, nullifier, seq, kind, choices, root, proof) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		b.EventID, b.Series, b.Index, b.Proof.Nullifier, b.Seq, b.Kind, choices, b.Proof.MerkleTreeRoot, proof); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return fmt.Errorf("%w: no such poll", ErrBallot)
		}
		return fmt.Errorf("insert ballot: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("cast ballot: %w", err)
	}
	return nil
}

// PublishBatch publishes the waiting ballots in the log, in a random order,
// then the new tally of each poll they belong to. It waits for minBallots
// ballots, unless the oldest has waited maxWait. It returns the polls whose
// results changed.
func (s *PGStore) PublishBatch(ctx context.Context, minBallots int, maxWait time.Duration) ([]int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("publish ballots: %w", err)
	}
	defer tx.Rollback(ctx)
	rows, err := tx.Query(ctx, `SELECT event_id, series, win, nullifier, seq, kind, choices, proof, received_at FROM anon_ballots WHERE published_at IS NULL FOR UPDATE`)
	if err != nil {
		return nil, fmt.Errorf("waiting ballots: %w", err)
	}
	var batch []AnonBallot
	oldest := s.now()
	for rows.Next() {
		var b AnonBallot
		var proof []byte
		var received time.Time
		if err := rows.Scan(&b.EventID, &b.Series, &b.Index, &b.Proof.Nullifier, &b.Seq, &b.Kind, &b.Choices, &proof, &received); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan waiting ballot: %w", err)
		}
		if err := json.Unmarshal(proof, &b.Proof); err != nil {
			rows.Close()
			return nil, fmt.Errorf("waiting ballot proof: %w", err)
		}
		if received.Before(oldest) {
			oldest = received
		}
		batch = append(batch, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("waiting ballots: %w", err)
	}
	if len(batch) == 0 || (len(batch) < minBallots && s.now().Sub(oldest) < maxWait) {
		return nil, nil
	}
	if err := shuffle(batch); err != nil {
		return nil, err
	}
	now := s.now()
	var events []int64
	for _, b := range batch {
		seq, err := appendLog(ctx, tx, LogBallot, b)
		if err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `UPDATE anon_ballots SET published_at = $6, log_seq = $7 WHERE event_id = $1 AND series = $2 AND win = $3 AND nullifier = $4 AND seq = $5`,
			b.EventID, b.Series, b.Index, b.Proof.Nullifier, b.Seq, now, seq); err != nil {
			return nil, fmt.Errorf("mark ballot published: %w", err)
		}
		if !slices.Contains(events, b.EventID) {
			events = append(events, b.EventID)
		}
	}
	for _, e := range events {
		w := ResultWindow(now)
		tallies, turnout, err := countBallots(ctx, tx, e, w)
		if err != nil {
			return nil, err
		}
		entry := tallyEntry{EventID: e, Window: w, Voters: turnout.Voters, Blank: turnout.Blank, BlankRanked: turnout.BlankRanked, BlankBeats: turnout.BlankBeats,
			First: map[int64]int{}, Ranked: map[int64]int{}, Beats: map[int64]map[int64]int{}}
		for _, t := range tallies {
			entry.First[t.PersonID], entry.Ranked[t.PersonID], entry.Beats[t.PersonID] = t.FirstChoices, t.Ranked, t.Beats
		}
		if _, err := appendLog(ctx, tx, LogTally, entry); err != nil {
			return nil, err
		}
		if err := pollUpdated(ctx, tx, e); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("publish ballots: %w", err)
	}
	return events, nil
}

// shuffle puts the ballots in a uniformly random order (Fisher-Yates, from
// crypto/rand: the order must not tell when a ballot came).
func shuffle(b []AnonBallot) error {
	for i := len(b) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			return fmt.Errorf("shuffle ballots: %w", err)
		}
		j := int(n.Int64())
		b[i], b[j] = b[j], b[i]
	}
	return nil
}

// Tallies returns the candidates of the event with their counts (no card),
// over the published ballots of the results' window: the last ballot of each
// nullifier, unless it withdrew (Tally: a candidate who left the poll is
// skipped, the next ones move up).
//
// The ballots and the log's size are read in one snapshot (repeatable
// read): a batch publishes its ballots and logs them in one transaction, so
// the count is exactly that of the log's first LogSize entries.
func (s *PGStore) Tallies(ctx context.Context, eventID int64) ([]CandidateTally, Turnout, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly, IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return nil, Turnout{}, fmt.Errorf("poll tallies: %w", err)
	}
	defer tx.Rollback(ctx)
	var size int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM vote_log`).Scan(&size); err != nil {
		return nil, Turnout{}, fmt.Errorf("poll tallies: log size: %w", err)
	}
	w := ResultWindow(s.now())
	out, turnout, err := countBallots(ctx, tx, eventID, w)
	turnout.LogSize, turnout.Window = size, &w
	return out, turnout, err
}

func countBallots(ctx context.Context, tx pgx.Tx, eventID int64, w Window) ([]CandidateTally, Turnout, error) {
	rows, err := tx.Query(ctx, `SELECT person_id FROM poll_candidates WHERE event_id = $1 ORDER BY person_id`, eventID)
	if err != nil {
		return nil, Turnout{}, fmt.Errorf("poll candidates of event %d: %w", eventID, err)
	}
	candidates, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, Turnout{}, fmt.Errorf("poll candidates of event %d: %w", eventID, err)
	}
	tally := NewTally(candidates)
	rows, err = tx.Query(ctx, `
		SELECT DISTINCT ON (nullifier) kind, choices FROM anon_ballots
		WHERE event_id = $1 AND series = $2 AND win = $3 AND published_at IS NOT NULL
		ORDER BY nullifier, seq DESC`, eventID, w.Series, w.Index)
	if err != nil {
		return nil, Turnout{}, fmt.Errorf("ballots of event %d: %w", eventID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var choices []int64
		if err := rows.Scan(&kind, &choices); err != nil {
			return nil, Turnout{}, fmt.Errorf("scan ballot: %w", err)
		}
		tally.Add(kind, choices)
	}
	if err := rows.Err(); err != nil {
		return nil, Turnout{}, fmt.Errorf("ballots of event %d: %w", eventID, err)
	}
	out, turnout := tally.Result()
	return out, turnout, nil
}

// Ballots are the published ballots of an open window of the event, the last
// of each nullifier: a voter's device finds its own among them, without
// telling which it looks for.
func (s *PGStore) Ballots(ctx context.Context, eventID int64, w Window) ([]AnonBallot, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (nullifier) nullifier, seq, kind, choices, log_seq FROM anon_ballots
		WHERE event_id = $1 AND series = $2 AND win = $3 AND published_at IS NOT NULL
		ORDER BY nullifier, seq DESC`, eventID, w.Series, w.Index)
	if err != nil {
		return nil, fmt.Errorf("ballots of event %d: %w", eventID, err)
	}
	defer rows.Close()
	out := []AnonBallot{}
	for rows.Next() {
		b := AnonBallot{EventID: eventID, Window: w}
		if err := rows.Scan(&b.Proof.Nullifier, &b.Seq, &b.Kind, &b.Choices, &b.LogSeq); err != nil {
			return nil, fmt.Errorf("scan ballot: %w", err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("ballots of event %d: %w", eventID, err)
	}
	return out, nil
}
