package polls

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
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

// LogEntry is one entry of the public log. Hash is
// SHA-256(prev_hash || seq as 8 bytes || kind || 0x00 || body): changing,
// dropping or reordering an entry changes every hash after it.
type LogEntry struct {
	Seq       int64           `json:"seq"`
	Kind      string          `json:"kind"`
	Body      json.RawMessage `json:"body"`
	PrevHash  string          `json:"prev_hash"`
	Hash      string          `json:"hash"`
	CreatedAt time.Time       `json:"created_at"`
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
	// Approvals and Favourites count, per candidate, the ballots that
	// approve them and that mark them as favourite.
	Approvals  map[int64]int `json:"approvals"`
	Favourites map[int64]int `json:"favourites"`
}

// voteLogLock serializes the appends (pg_advisory_xact_lock key).
const voteLogLock = 7_464_201

// entryHash is the hash of an entry, given the one before.
func entryHash(prev []byte, seq int64, kind string, body []byte) []byte {
	h := sha256.New()
	h.Write(prev)
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(seq))
	h.Write(n[:])
	h.Write([]byte(kind))
	h.Write([]byte{0})
	h.Write(body)
	return h.Sum(nil)
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
	prev := make([]byte, sha256.Size)
	err = tx.QueryRow(ctx, `SELECT seq, hash FROM vote_log ORDER BY seq DESC LIMIT 1`).Scan(&seq, &prev)
	if err != nil && err != pgx.ErrNoRows {
		return 0, fmt.Errorf("vote log head: %w", err)
	}
	seq++
	hash := entryHash(prev, seq, kind, raw)
	if _, err := tx.Exec(ctx, `INSERT INTO vote_log (seq, kind, body, prev_hash, hash) VALUES ($1, $2, $3, $4, $5)`,
		seq, kind, string(raw), prev, hash); err != nil {
		return 0, fmt.Errorf("append vote log %s: %w", kind, err)
	}
	return seq, nil
}

// maxLogPage bounds the entries of one Log call.
const maxLogPage = 1000

// Log returns the entries after the seq, in order, at most limit.
func (s *PGStore) Log(ctx context.Context, after int64, limit int) ([]LogEntry, error) {
	if limit <= 0 || limit > maxLogPage {
		limit = maxLogPage
	}
	rows, err := s.pool.Query(ctx, `SELECT seq, kind, body, prev_hash, hash, created_at FROM vote_log WHERE seq > $1 ORDER BY seq LIMIT $2`, after, limit)
	if err != nil {
		return nil, fmt.Errorf("read vote log: %w", err)
	}
	defer rows.Close()
	out := []LogEntry{}
	for rows.Next() {
		var e LogEntry
		var body string
		var prev, hash []byte
		if err := rows.Scan(&e.Seq, &e.Kind, &body, &prev, &hash, &e.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan vote log: %w", err)
		}
		e.Body, e.PrevHash, e.Hash = json.RawMessage(body), hex.EncodeToString(prev), hex.EncodeToString(hash)
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read vote log: %w", err)
	}
	return out, nil
}

// VerifyLog recomputes the hash chain of the entries, which must follow the
// entry whose hash is prev (all zeros before the first): it returns the
// first entry that does not match.
func VerifyLog(prev []byte, entries []LogEntry) error {
	for _, e := range entries {
		want := entryHash(prev, e.Seq, e.Kind, e.Body)
		if hex.EncodeToString(prev) != e.PrevHash || hex.EncodeToString(want) != e.Hash {
			return fmt.Errorf("vote log entry %d does not chain", e.Seq)
		}
		prev = want
	}
	return nil
}
