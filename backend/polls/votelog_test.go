package polls_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"

	"digital-mirror/backend/polls"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/require"
	"golang.org/x/mod/sumdb/tlog"
)

func openLog(t *testing.T) *polls.PGStore {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set, skipping database tests")
	}
	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	t.Cleanup(storer.Close)
	_, err = storer.Pool().Exec(ctx, "TRUNCATE vote_log, vote_log_hashes, vote_log_stamps")
	require.NoError(t, err)
	return polls.NewPGStore(storer.Pool())
}

// TestLogTreeProofs: every head proves each entry it holds and that it
// extends every head before it; a signed checkpoint opens with the
// verifier key only; the tree of entries logged before it is filled again.
func TestLogTreeProofs(t *testing.T) {
	s := openLog(t)
	ctx := context.Background()
	var heads []polls.LogTree
	for i := 0; i < 7; i++ {
		body, _ := json.Marshal(map[string]any{"v": 2, "n": i})
		seq, err := s.Append(ctx, "seen", body)
		require.NoError(t, err)
		require.Equal(t, int64(i+1), seq)
		h, err := s.Head(ctx)
		require.NoError(t, err)
		heads = append(heads, h)
	}
	entries, err := s.Log(ctx, 0, 100)
	require.NoError(t, err)
	require.Len(t, entries, 7)
	for _, h := range heads {
		for _, e := range entries[:h.N] {
			p, err := s.ProveEntry(ctx, h.N, e.Seq)
			require.NoError(t, err)
			require.NoError(t, tlog.CheckRecord(p, h.N, h.Hash, e.Seq-1, tlog.RecordHash(polls.LeafData(e.Seq, e.Kind, []byte(e.Body)))))
		}
		for _, older := range heads {
			if older.N >= h.N {
				continue
			}
			p, err := s.ProveAppend(ctx, h.N, older.N)
			require.NoError(t, err)
			require.NoError(t, tlog.CheckTree(p, h.N, h.Hash, older.N, older.Hash))
		}
	}
	// The tree from the entries alone equals the stored one.
	rebuilt, err := polls.TreeOf(entries)
	require.NoError(t, err)
	require.Equal(t, heads[6], rebuilt)

	skey, vkey, err := polls.GenerateLogKey()
	require.NoError(t, err)
	signer, err := polls.NewLogSigner(skey)
	require.NoError(t, err)
	note, err := signer.SignText(polls.CheckpointText(heads[6]) + "tree 0x00 0\nindex 1\n")
	require.NoError(t, err)
	got, err := polls.OpenCheckpoint(note, vkey)
	require.NoError(t, err)
	require.Equal(t, heads[6], got)
	_, otherKey, _ := polls.GenerateLogKey()
	_, err = polls.OpenCheckpoint(note, otherKey)
	require.Error(t, err)

	// Refill: with the hashes gone, FillTree rebuilds the same tree.
	_, err = s.Pool().Exec(ctx, "TRUNCATE vote_log_hashes")
	require.NoError(t, err)
	require.NoError(t, s.FillTree(ctx))
	again, err := s.Head(ctx)
	require.NoError(t, err)
	require.Equal(t, heads[6], again, fmt.Sprintf("after refill: %+v", again))
}
