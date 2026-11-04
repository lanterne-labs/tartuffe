package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"digital-mirror/backend/core"
)

// TestCandidateFlag checks the flag and that the maintenance queries follow it:
// a person with new facts is listed only while a candidate.
func TestCandidateFlag(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	id, err := storer.FindOrInsertPersonWithType(ctx, "candidate flag testperson", "human")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = storer.pool.Exec(ctx, `DELETE FROM persons WHERE id = $1`, id) })

	ok, err := storer.IsCandidate(ctx, id)
	require.NoError(t, err)
	require.False(t, ok, "a new person is not a candidate")

	require.NoError(t, storer.SetCandidate(ctx, id, true))
	ok, err = storer.IsCandidate(ctx, id)
	require.NoError(t, err)
	require.True(t, ok)
	list, err := storer.ListCandidates(ctx)
	require.NoError(t, err)
	found := false
	for _, p := range list {
		found = found || p.ID == id
	}
	require.True(t, found, "ListCandidates lists the flagged person")

	ranked, err := storer.GetPersonsRankedByScore(ctx, 10, 0, "", "candidate flag testperson", "", 0, "true")
	require.NoError(t, err)
	require.Len(t, ranked, 1)
	require.True(t, ranked[0].Candidate)
	ranked, err = storer.GetPersonsRankedByScore(ctx, 10, 0, "", "candidate flag testperson", "", 0, "false")
	require.NoError(t, err)
	require.Empty(t, ranked)

	require.NoError(t, storer.SetCandidate(ctx, id, false))
	persons, err := storer.PersonsWithFactsSince(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	for _, p := range persons {
		require.NotEqual(t, id, p.ID, "an unflagged person is left out of the pass")
	}
	require.Error(t, storer.SetCandidate(ctx, id+1_000_000, true), "an unknown person is an error")
}

// TestInsertFactWithoutEmbedding: a fact of a person who is not a candidate is
// stored without its embedding (core.FactInserter drops it); the insert must
// write NULL, not an empty halfvec, which pgvector rejects. From 2026-11-02 to
// 2026-11-04 every such fact failed this way (the feed's votes and debates).
func TestInsertFactWithoutEmbedding(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	id, err := storer.InsertFact(ctx, core.StoreFactInput{
		NormalizedFactText: "fact without embedding testcase",
		EmbeddingText:      "fact without embedding testcase",
		Lang:               "fr",
	})
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = storer.pool.Exec(ctx, `DELETE FROM facts WHERE id = $1`, id) })
	var isNull bool
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT embedding IS NULL FROM facts WHERE id = $1`, id).Scan(&isNull))
	require.True(t, isNull)
}
