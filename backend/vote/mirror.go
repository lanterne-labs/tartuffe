package vote

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"digital-mirror/backend/vote/ballot"
)

// Mirror is the public data mirror of SPEC §8.3: the files browsers read
// instead of talking to our server. The ingest stages files at each
// checkpoint and commits them; the commit token goes into the checkpoint
// note ("mirror <token>") so a browser fetches the files of exactly that
// checkpoint. DirMirror writes a directory (served by any static host or
// the dev server); a git-backed mirror commits and pushes to a public
// repository read through a CDN.
type Mirror interface {
	// Read returns a staged or committed file, nil when there is none.
	Read(path string) ([]byte, error)
	// Stage sets a file's content for the next commit.
	Stage(path string, data []byte) error
	// Commit publishes the staged files and returns the token naming them.
	Commit(ctx context.Context, message string) (string, error)
}

// DirMirror is a Mirror on a directory: Stage writes the file, Commit
// returns a hash of the files written since the last commit.
type DirMirror struct {
	Dir     string
	pending []string
}

// Read implements Mirror.
func (m *DirMirror) Read(path string) ([]byte, error) {
	b, err := os.ReadFile(filepath.Join(m.Dir, filepath.FromSlash(path)))
	if os.IsNotExist(err) {
		return nil, nil
	}
	return b, err
}

// Stage implements Mirror.
func (m *DirMirror) Stage(path string, data []byte) error {
	full := filepath.Join(m.Dir, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	tmp := full + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, full); err != nil {
		return err
	}
	m.pending = append(m.pending, path)
	return nil
}

// Commit implements Mirror.
func (m *DirMirror) Commit(ctx context.Context, message string) (string, error) {
	h := sha256.New()
	sort.Strings(m.pending)
	for _, p := range m.pending {
		b, _ := m.Read(p)
		h.Write([]byte(p))
		h.Write(b)
	}
	m.pending = nil
	return "dir-" + hex.EncodeToString(h.Sum(nil))[:16], nil
}

// The mirror's files.
const (
	blockSize = 256
	groupSize = 16 * blockSize
	nibbles   = "0123456789abcdef"
)

// MirrorLeaf is a leaf as the mirror lists it.
type MirrorLeaf struct {
	Position   int    `json:"position"`
	ID         string `json:"id"`
	Checkpoint int64  `json:"checkpoint"`
}

// MirrorEntry is a log entry as the hourly entries files list it: what a
// browser needs to find its receipt.
type MirrorEntry struct {
	Checkpoint int64  `json:"checkpoint"`
	Kind       string `json:"kind"`
	Hash       string `json:"hash"` // pi_hash for ballots, body hash otherwise
	LogSeq     int64  `json:"log_seq"`
}

// stageMirror writes the files of a checkpoint: the leaves it added, the
// tree files, the entries of its hour, the index shards it touched, the
// current setup and tallies. It returns the commit token.
func (in *Ingest) stageMirror(ctx context.Context, index int64, prevLogSize, logSize int64, now time.Time) (string, error) {
	m := in.cfg.Mirror
	if m == nil {
		return "", nil
	}
	stageJSON := func(path string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		return m.Stage(path, b)
	}
	// New leaves: by day, the open block, the full groups, the roots.
	leaves, err := in.store.LeavesBetween(ctx, in.cfg.Event.ID, prevLogSize, logSize)
	if err != nil {
		return "", err
	}
	if len(leaves) > 0 {
		day := now.UTC().Format("2006-01-02")
		var today []MirrorLeaf
		if b, _ := m.Read("leaves/" + day + ".json"); b != nil {
			_ = json.Unmarshal(b, &today)
		}
		touched := map[string]bool{}
		for _, l := range leaves {
			today = append(today, MirrorLeaf{Position: l.Position, ID: l.ID, Checkpoint: index})
			touched[strings.ToLower(l.ID[2:3])] = true
		}
		if err := stageJSON("leaves/"+day+".json", today); err != nil {
			return "", err
		}
		size := in.tree.Size()
		open := size - size%blockSize
		openLeaves, err := in.store.LeavesRange(ctx, in.cfg.Event.ID, open, size)
		if err != nil {
			return "", err
		}
		if err := stageJSON("blocks/open.json", openLeaves); err != nil {
			return "", err
		}
		if err := stageJSON("blocks/roots.json", in.tree.BlockRoots(blockSize)); err != nil {
			return "", err
		}
		// A group file once its 16 blocks are full.
		firstNew := leaves[0].Position
		for g := firstNew / groupSize; (g+1)*groupSize <= size; g++ {
			gl, err := in.store.LeavesRange(ctx, in.cfg.Event.ID, g*groupSize, (g+1)*groupSize)
			if err != nil {
				return "", err
			}
			if err := stageJSON(fmt.Sprintf("blocks/%d.json", g), gl); err != nil {
				return "", err
			}
		}
		for n := range touched {
			shard, err := in.store.PositionsShard(ctx, in.cfg.Event.ID, n)
			if err != nil {
				return "", err
			}
			if err := stageJSON("positions/"+n+".json", shard); err != nil {
				return "", err
			}
		}
	}
	// The entries of this checkpoint, into the hour's file.
	entries, err := in.store.EntriesOf(ctx, index)
	if err != nil {
		return "", err
	}
	if len(entries) > 0 {
		hour := now.UTC().Format("2006-01-02T15")
		var list []MirrorEntry
		if b, _ := m.Read("entries/" + hour + ".json"); b != nil {
			_ = json.Unmarshal(b, &list)
		}
		list = append(list, entries...)
		if err := stageJSON("entries/"+hour+".json", list); err != nil {
			return "", err
		}
		touched := map[string]bool{}
		for _, e := range entries {
			if e.Kind == KindBallot {
				touched[strings.ToLower(e.Hash[2:4])] = true
			}
		}
		_ = touched // the index shards are keyed by nullifier, below
	}
	// The index shards of the pseudonyms that voted in this checkpoint.
	nulls, err := in.store.NullifiersOf(ctx, index)
	if err != nil {
		return "", err
	}
	shards := map[string]bool{}
	for _, n := range nulls {
		shards[strings.ToLower(n[2:4])] = true
	}
	for s := range shards {
		idx, err := in.store.IndexShard(ctx, in.cfg.Event.ID, s)
		if err != nil {
			return "", err
		}
		if err := stageJSON("index/"+s+".json", idx); err != nil {
			return "", err
		}
	}
	// The setup in force and the latest tally.
	cur := in.current(now)
	if err := stageJSON("setup.json", struct {
		Seq   int64 `json:"seq"`
		Setup Setup `json:"setup"`
	}{cur.Seq, cur.Setup}); err != nil {
		return "", err
	}
	if lt, err := in.store.LastTally(ctx, in.cfg.Event.ID); err == nil && lt != nil {
		if b, _ := m.Read(fmt.Sprintf("tallies/%d.json", lt.Seq)); b == nil {
			vals, err := in.store.TallyValues(lt)
			var res *Results
			if err == nil {
				r := ComputeResults(vals, &cur.Setup)
				res = &r
			}
			out := struct {
				Seq     int64      `json:"seq"`
				At      time.Time  `json:"at"`
				Tally   TallyEntry `json:"tally"`
				Results *Results   `json:"results,omitempty"`
			}{lt.Seq, lt.At, lt.Body, res}
			if err := stageJSON(fmt.Sprintf("tallies/%d.json", lt.Seq), out); err != nil {
				return "", err
			}
			if err := stageJSON("tallies/latest.json", out); err != nil {
				return "", err
			}
		}
	}
	token, err := m.Commit(ctx, fmt.Sprintf("checkpoint %d", index))
	if err != nil {
		return "", err
	}
	log.Debug().Int64("checkpoint", index).Str("token", token).Int("leaves", len(leaves)).Int("entries", len(entries)).Msg("vote: mirror staged")
	return token, nil
}

// stageNote writes a signed checkpoint note where browsers find the
// latest one (and by index).
func (in *Ingest) stageNote(index int64, note []byte) error {
	m := in.cfg.Mirror
	if m == nil {
		return nil
	}
	if err := m.Stage(fmt.Sprintf("checkpoints/%d.txt", index), note); err != nil {
		return err
	}
	return m.Stage("checkpoint.txt", note)
}

// TallyValues unpacks a logged tally's totals (its proofs are the
// auditor's to check: the mirror shows what the log says).
func (s *Store) TallyValues(lt *LoggedTally) ([ballot.V]int64, error) {
	var P [ballot.J]*big.Int
	for j := range P {
		x, ok := new(big.Int).SetString(strings.TrimPrefix(lt.Body.P[j], "0x"), 16)
		if !ok {
			return [ballot.V]int64{}, fmt.Errorf("tally p[%d]", j)
		}
		P[j] = x
	}
	return ballot.Unpack(P, lt.Body.Count)
}
