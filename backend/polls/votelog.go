package polls

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/mod/sumdb/tlog"
)

// The kinds of the public log's entries.
const (
	// LogRegister: a document admitted a commitment (body: registerEntry).
	LogRegister = "register"
	// LogRoot: a window's voter tree reached a root (body: rootEntry).
	LogRoot = "root"
	// LogBallot: a ballot, published in a shuffled batch (body: AnonBallot).
	LogBallot = "ballot"
	// LogTally: the results of a poll after a batch (body: tallyEntry).
	LogTally = "tally"
)

// LogEntry is one entry of the public log. The entries are the records of a
// Merkle tree (the tree of Certificate Transparency, RFC 6962: package
// tlog), record Seq-1 holding LeafData(Seq, Kind, Body): a signed checkpoint
// of the tree (checkpoint.go) commits to every entry before it, and a later
// checkpoint proves it only appended to the earlier one. Body is JSON, kept
// as its text: the tree hashes these very bytes.
type LogEntry struct {
	Seq       int64     `json:"seq"`
	Kind      string    `json:"kind"`
	Body      string    `json:"body"`
	CreatedAt time.Time `json:"created_at"`
}

// LeafData is the record of an entry in the tree:
//
//	tartuffe-log/v1\n<seq>\n<kind>\n<body>
func LeafData(seq int64, kind string, body []byte) []byte {
	return append([]byte("tartuffe-log/v1\n"+strconv.FormatInt(seq, 10)+"\n"+kind+"\n"), body...)
}

// registerEntry is a registration: the document (an HMAC of its
// identifier, never the identifier) admits the commitment from the windows
// From; Replaces is the commitment it had, which stays until those windows.
type registerEntry struct {
	Document   string `json:"document"`
	Provider   string `json:"provider"`
	Commitment string `json:"commitment"`
	From       [2]int `json:"from"`
	Replaces   string `json:"replaces,omitempty"`
}

type rootEntry struct {
	Window
	Root string `json:"root"`
	Size int    `json:"size"`
}

type tallyEntry struct {
	EventID int64 `json:"poll"`
	Window
	Voters int `json:"voters"`
	Blank  int `json:"blank"`
	// First and Ranked count, per candidate, the ballots that rank them
	// first and at all; Beats[a][b] those that rank a above b.
	First  map[int64]int           `json:"first"`
	Ranked map[int64]int           `json:"ranked"`
	Beats  map[int64]map[int64]int `json:"beats"`
}

// voteLogLock serializes the appends (pg_advisory_xact_lock key).
const voteLogLock = 7_464_201

// querier is a transaction or the pool.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// hashReader reads the tree's stored hashes (vote_log_hashes, by tlog's
// stored hash index).
func hashReader(ctx context.Context, q querier) tlog.HashReaderFunc {
	return func(indexes []int64) ([]tlog.Hash, error) {
		rows, err := q.Query(ctx, `SELECT idx, hash FROM vote_log_hashes WHERE idx = ANY($1)`, indexes)
		if err != nil {
			return nil, fmt.Errorf("vote log hashes: %w", err)
		}
		defer rows.Close()
		byIdx := make(map[int64]tlog.Hash, len(indexes))
		for rows.Next() {
			var idx int64
			var h []byte
			if err := rows.Scan(&idx, &h); err != nil {
				return nil, fmt.Errorf("scan vote log hash: %w", err)
			}
			var hh tlog.Hash
			copy(hh[:], h)
			byIdx[idx] = hh
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("vote log hashes: %w", err)
		}
		out := make([]tlog.Hash, len(indexes))
		for i, idx := range indexes {
			h, ok := byIdx[idx]
			if !ok {
				return nil, fmt.Errorf("vote log hash %d is missing", idx)
			}
			out[i] = h
		}
		return out, nil
	}
}

// storeLeaf adds record n (the entry of seq n+1) to the tree.
func storeLeaf(ctx context.Context, tx pgx.Tx, n int64, data []byte) error {
	hashes, err := tlog.StoredHashes(n, data, hashReader(ctx, tx))
	if err != nil {
		return fmt.Errorf("vote log tree: %w", err)
	}
	base := tlog.StoredHashIndex(0, n)
	for i, h := range hashes {
		if _, err := tx.Exec(ctx, `INSERT INTO vote_log_hashes (idx, hash) VALUES ($1, $2)`, base+int64(i), h[:]); err != nil {
			return fmt.Errorf("store vote log hash: %w", err)
		}
	}
	return nil
}

// appendLog appends an entry in the transaction and returns its seq.
func appendLog(ctx context.Context, tx pgx.Tx, kind string, body any) (int64, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, fmt.Errorf("vote log %s body: %w", kind, err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, voteLogLock); err != nil {
		return 0, fmt.Errorf("lock vote log: %w", err)
	}
	var seq int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM vote_log`).Scan(&seq); err != nil {
		return 0, fmt.Errorf("vote log head: %w", err)
	}
	seq++
	if _, err := tx.Exec(ctx, `INSERT INTO vote_log (seq, kind, body) VALUES ($1, $2, $3)`, seq, kind, string(raw)); err != nil {
		return 0, fmt.Errorf("append vote log %s: %w", kind, err)
	}
	if err := storeLeaf(ctx, tx, seq-1, LeafData(seq, kind, raw)); err != nil {
		return 0, err
	}
	return seq, nil
}

// LogTree is the tree of the log's first Size entries.
type LogTree = tlog.Tree

// Head returns the tree of the whole log.
func (s *PGStore) Head(ctx context.Context) (LogTree, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return LogTree{}, fmt.Errorf("vote log head: %w", err)
	}
	defer tx.Rollback(ctx)
	var n int64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM vote_log`).Scan(&n); err != nil {
		return LogTree{}, fmt.Errorf("vote log head: %w", err)
	}
	h, err := tlog.TreeHash(n, hashReader(ctx, tx))
	if err != nil {
		return LogTree{}, fmt.Errorf("vote log head: %w", err)
	}
	return LogTree{N: n, Hash: h}, nil
}

// ProveEntry proves that the tree of size entries holds the entry seq.
func (s *PGStore) ProveEntry(ctx context.Context, size, seq int64) (tlog.RecordProof, error) {
	if seq < 1 || seq > size {
		return nil, fmt.Errorf("%w: entry %d is not in a tree of %d", ErrBadProofRequest, seq, size)
	}
	if err := s.checkSize(ctx, size); err != nil {
		return nil, err
	}
	p, err := tlog.ProveRecord(size, seq-1, hashReader(ctx, s.pool))
	if err != nil {
		return nil, fmt.Errorf("prove vote log entry: %w", err)
	}
	return p, nil
}

// ProveAppend proves that the tree of size entries extends the tree of old
// entries.
func (s *PGStore) ProveAppend(ctx context.Context, size, old int64) (tlog.TreeProof, error) {
	if old < 1 || old > size {
		return nil, fmt.Errorf("%w: a tree of %d does not extend one of %d", ErrBadProofRequest, size, old)
	}
	if err := s.checkSize(ctx, size); err != nil {
		return nil, err
	}
	p, err := tlog.ProveTree(size, old, hashReader(ctx, s.pool))
	if err != nil {
		return nil, fmt.Errorf("prove vote log append: %w", err)
	}
	return p, nil
}

// ErrBadProofRequest refuses a proof of trees the log never had.
var ErrBadProofRequest = fmt.Errorf("no such proof")

func (s *PGStore) checkSize(ctx context.Context, size int64) error {
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT COALESCE(MAX(seq), 0) FROM vote_log`).Scan(&n); err != nil {
		return fmt.Errorf("vote log head: %w", err)
	}
	if size > n {
		return fmt.Errorf("%w: the log has %d entries, not %d", ErrBadProofRequest, n, size)
	}
	return nil
}

// FillTree hashes the entries the tree lacks (the log of before the tree):
// run at start, before any append.
func (s *PGStore) FillTree(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("fill vote log tree: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, voteLogLock); err != nil {
		return fmt.Errorf("lock vote log: %w", err)
	}
	var stored int64
	if err := tx.QueryRow(ctx, `SELECT COUNT(*) FROM vote_log_hashes`).Scan(&stored); err != nil {
		return fmt.Errorf("fill vote log tree: %w", err)
	}
	var hashed int64
	for tlog.StoredHashCount(hashed+1) <= stored {
		hashed++
	}
	rows, err := tx.Query(ctx, `SELECT seq, kind, body FROM vote_log WHERE seq > $1 ORDER BY seq`, hashed)
	if err != nil {
		return fmt.Errorf("fill vote log tree: %w", err)
	}
	type entry struct {
		seq        int64
		kind, body string
	}
	var todo []entry
	for rows.Next() {
		var e entry
		if err := rows.Scan(&e.seq, &e.kind, &e.body); err != nil {
			rows.Close()
			return fmt.Errorf("fill vote log tree: %w", err)
		}
		todo = append(todo, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("fill vote log tree: %w", err)
	}
	for _, e := range todo {
		if e.seq != hashed+1 {
			return fmt.Errorf("fill vote log tree: entry %d follows %d", e.seq, hashed)
		}
		if err := storeLeaf(ctx, tx, hashed, LeafData(e.seq, e.kind, []byte(e.body))); err != nil {
			return err
		}
		hashed++
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("fill vote log tree: %w", err)
	}
	return nil
}

// maxLogPage bounds the entries of one Log call.
const maxLogPage = 1000

// Log returns the entries after the seq, in order, at most limit.
func (s *PGStore) Log(ctx context.Context, after int64, limit int) ([]LogEntry, error) {
	if limit <= 0 || limit > maxLogPage {
		limit = maxLogPage
	}
	rows, err := s.pool.Query(ctx, `SELECT seq, kind, body, created_at FROM vote_log WHERE seq > $1 ORDER BY seq LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read vote log: %w", err)
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		var e LogEntry
		if err := rows.Scan(&e.Seq, &e.Kind, &e.Body, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan vote log: %w", err)
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read vote log: %w", err)
	}
	return out, nil
}

// TreeOf is the tree of the entries, which must be the log's first ones in
// order: what anyone recomputes from the downloaded log.
func TreeOf(entries []LogEntry) (LogTree, error) {
	var hashes []tlog.Hash
	r := tlog.HashReaderFunc(func(idx []int64) ([]tlog.Hash, error) {
		out := make([]tlog.Hash, len(idx))
		for i, x := range idx {
			if x >= int64(len(hashes)) {
				return nil, fmt.Errorf("hash %d is missing", x)
			}
			out[i] = hashes[x]
		}
		return out, nil
	})
	for i, e := range entries {
		if e.Seq != int64(i)+1 {
			return LogTree{}, fmt.Errorf("vote log entry %d is at place %d", e.Seq, i+1)
		}
		hs, err := tlog.StoredHashes(int64(i), LeafData(e.Seq, e.Kind, []byte(e.Body)), r)
		if err != nil {
			return LogTree{}, err
		}
		hashes = append(hashes, hs...)
	}
	n := int64(len(entries))
	h, err := tlog.TreeHash(n, r)
	if err != nil {
		return LogTree{}, err
	}
	return LogTree{N: n, Hash: h}, nil
}
