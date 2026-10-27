package vote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	"golang.org/x/mod/sumdb/tlog"
)

// Snapshot is the close snapshot of SPEC §9.3: the Merkle root over the
// sorted ids of every protocol event our server had seen at the close.
// It is posted to every box, logged as a `snapshot` entry (what the
// tallier's final tally covers: every ballot logged before it), and its
// list is on the mirror (snapshot.json). Events seen after it are rejected
// with `closed`.
type Snapshot struct {
	V          int       `json:"v"`
	Event      int64     `json:"event"`
	At         time.Time `json:"at"`
	Root       string    `json:"root"`
	Count      int       `json:"count"`
	Checkpoint int64     `json:"checkpoint"` // the last checkpoint before the snapshot
}

// SnapshotList is the mirror's snapshot.json: the snapshot and its ids.
type SnapshotList struct {
	Snapshot
	IDs []string `json:"ids"`
}

// SnapshotRoot is the Merkle root (RFC 6962, the log's hashing) over the
// ids sorted; it sorts ids in place.
func SnapshotRoot(ids []string) string {
	sort.Strings(ids)
	records := make([]tlog.Hash, len(ids))
	for i, id := range ids {
		records[i] = tlog.RecordHash([]byte(id))
	}
	h, err := tlog.TreeHash(int64(len(records)), tlog.HashReaderFunc(func(idx []int64) ([]tlog.Hash, error) {
		return storedHashes(records, idx)
	}))
	if err != nil {
		// storedHashes fails only beyond the records, which TreeHash never asks.
		panic(err)
	}
	return "0x" + hexOf(h[:])
}

// Close takes the close snapshot once the close has passed: logs what is
// pending, snapshots every event seen, logs and posts the snapshot, then
// checkpoints it. Nothing happens before the close or twice.
func (in *Ingest) Close(ctx context.Context) error {
	in.mu.Lock()
	defer in.mu.Unlock()
	now := in.cfg.Now()
	if in.snapshotSeq != 0 || now.Before(in.current(now).Setup.Closes) {
		return nil
	}
	if err := in.checkpoint(ctx); err != nil {
		return fmt.Errorf("the checkpoint before the snapshot: %w", err)
	}
	ids, err := in.store.HandledIDs(ctx)
	if err != nil {
		return err
	}
	snap := Snapshot{V: Version, Event: in.cfg.Event.ID, At: now.UTC(), Root: SnapshotRoot(ids), Count: len(ids), Checkpoint: in.nextIndex - 1}
	body, err := Canonical(snap)
	if err != nil {
		return err
	}
	if m := in.cfg.Mirror; m != nil {
		list, _ := json.Marshal(SnapshotList{Snapshot: snap, IDs: ids})
		if err := m.Stage("snapshot.json", list); err != nil {
			return fmt.Errorf("mirror: %w", err)
		}
	}
	seq, err := in.store.Log.Append(ctx, KindSnapshot, body)
	if err != nil {
		return err
	}
	in.snapshotSeq = seq
	n := in.cfg.Mail.PostAll(ctx, Outgoing{Kind: OutSnapshot, Body: string(body)})
	log.Info().Int64("seq", seq).Str("root", snap.Root).Int("events", len(ids)).Int("boxes", n).Msg("vote: close snapshot")
	return in.checkpoint(ctx)
}

// LoggedSnapshot is the snapshot entry with its place in the log.
type LoggedSnapshot struct {
	Seq  int64
	Body Snapshot
}

// SnapshotEntry returns the event's snapshot entry, nil while open.
func (s *Store) SnapshotEntry(ctx context.Context, eventID int64) (*LoggedSnapshot, error) {
	rows, err := s.db.Query(ctx, `SELECT seq, body FROM vote_log WHERE kind = $1 ORDER BY seq DESC`, KindSnapshot)
	if err != nil {
		return nil, fmt.Errorf("snapshot: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var ls LoggedSnapshot
		var body string
		if err := rows.Scan(&ls.Seq, &body); err != nil {
			return nil, err
		}
		if json.Unmarshal([]byte(body), &ls.Body) == nil && ls.Body.Event == eventID {
			return &ls, nil
		}
	}
	return nil, rows.Err()
}

// HandledIDs lists the id of every event handled, in no order.
func (s *Store) HandledIDs(ctx context.Context) ([]string, error) {
	rows, err := s.db.Query(ctx, `SELECT nostr_id FROM vote2_events`)
	if err != nil {
		return nil, fmt.Errorf("handled ids: %w", err)
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// parseSnapshotRoot checks a root's form.
func parseSnapshotRoot(root string) error {
	if len(root) != 66 || root[:2] != "0x" {
		return errors.New("vote: snapshot root is not 32 bytes of hex")
	}
	if _, err := strconv.ParseUint(root[2:18], 16, 64); err != nil {
		return errors.New("vote: snapshot root is not hex")
	}
	return nil
}
