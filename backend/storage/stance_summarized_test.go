package storage

import (
	"context"
	"digital-mirror/backend/core"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestStanceSummarizedAtCursor pins the incremental-refresh cursor: a fresh
// stance reports every member as unsummarized; UpdateStanceMetadata stamps
// summarized_at so those members drop out; a membership added afterwards is
// the only thing reported next; merged-in memberships get a fresh added_at so
// they show up as new to the destination.
func TestStanceSummarizedAtCursor(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	truncateFanoutTables(t, storer)

	personID, _, err := storer.AddPerson(ctx, "Summarized Cursor")
	require.NoError(t, err)
	insertFact := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, canonical_verb) VALUES ($1, 'said') RETURNING id", text).Scan(&id))
		return id
	}
	mkStance := func(label string, factIDs ...int64) int64 {
		id, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
			PersonID: personID, ClusterLabel: label, ClusterEmbedding: makeUnitEmbedding(0),
			Summary: "placeholder", KeyFactIDs: factIDs, FactCount: len(factIDs),
		})
		require.NoError(t, err)
		for _, f := range factIDs {
			require.NoError(t, storer.AddFactToStance(ctx, id, f))
		}
		return id
	}

	f1, f2 := insertFact("first"), insertFact("second")
	stanceID := mkStance("topic", f1, f2)

	// Never summarized: everything is new, oldest membership first.
	pending, err := storer.GetUnsummarizedFactsForStance(ctx, stanceID)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	require.Equal(t, f1, pending[0].ID)
	require.Equal(t, "first", pending[0].NormalizedFactText)
	require.Equal(t, "said", pending[0].CanonicalVerb)

	// A refresh advances summarized_at over the consumed facts; nothing is
	// pending afterwards.
	require.NoError(t, storer.UpdateStanceMetadata(ctx, stanceID, "topic", "summary v1", []int64{f1, f2}))
	label, summary, err := storer.GetStanceMetadata(ctx, stanceID)
	require.NoError(t, err)
	require.Equal(t, "topic", label)
	require.Equal(t, "summary v1", summary)
	pending, err = storer.GetUnsummarizedFactsForStance(ctx, stanceID)
	require.NoError(t, err)
	require.Empty(t, pending)

	// A later membership is the only pending fact.
	f3 := insertFact("third")
	require.NoError(t, storer.AddFactToStance(ctx, stanceID, f3))
	pending, err = storer.GetUnsummarizedFactsForStance(ctx, stanceID)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, f3, pending[0].ID)

	// Race guard: a refresh that consumed only f1+f2 (it was running when f3
	// joined) must NOT stamp past f3 — f3 stays pending.
	require.NoError(t, storer.UpdateStanceMetadata(ctx, stanceID, "topic", "summary v1b", []int64{f1, f2}))
	pending, err = storer.GetUnsummarizedFactsForStance(ctx, stanceID)
	require.NoError(t, err)
	require.Len(t, pending, 1, "a fact added during the refresh is still pending")
	require.Equal(t, f3, pending[0].ID)

	// Merge: src's old memberships arrive in dst with a fresh added_at, so they
	// are new to dst's summary even though they predate dst's summarized_at.
	fOld := insertFact("old in src")
	srcID := mkStance("src topic", fOld)
	_, err = storer.pool.Exec(ctx,
		"UPDATE stance_document_facts SET added_at = NOW() - INTERVAL '30 days' WHERE stance_document_id = $1", srcID)
	require.NoError(t, err)
	require.NoError(t, storer.UpdateStanceMetadata(ctx, stanceID, "topic", "summary v2", []int64{f3})) // absorbs f3
	require.NoError(t, storer.MergeStancesForPerson(ctx, srcID, stanceID, makeUnitEmbedding(0)))
	pending, err = storer.GetUnsummarizedFactsForStance(ctx, stanceID)
	require.NoError(t, err)
	require.Len(t, pending, 1, "only the merged-in fact is new to dst")
	require.Equal(t, fOld, pending[0].ID)
}

// TestGetStanceTensions pins the tension lookup: contradictions between two
// facts of the stance are reported under BOTH facts with the other's text;
// contradictions with facts outside the stance, or owned by another person,
// are not.
func TestGetStanceTensions(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	truncateFanoutTables(t, storer)
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE contradictions RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personID, _, err := storer.AddPerson(ctx, "Tension Owner")
	require.NoError(t, err)
	otherID, _, err := storer.AddPerson(ctx, "Tension Other")
	require.NoError(t, err)
	insertFact := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text) VALUES ($1) RETURNING id", text).Scan(&id))
		return id
	}
	fA, fB, fC, fOut := insertFact("promised X"), insertFact("did not-X"), insertFact("unrelated"), insertFact("outside the stance")
	stanceID, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID: personID, ClusterLabel: "t", ClusterEmbedding: makeUnitEmbedding(0),
		Summary: "s", KeyFactIDs: []int64{fA, fB, fC}, FactCount: 3,
	})
	require.NoError(t, err)
	for _, f := range []int64{fA, fB, fC} {
		require.NoError(t, storer.AddFactToStance(ctx, stanceID, f))
	}
	ins := func(pid, a, b int64, reason string) {
		if a > b {
			a, b = b, a
		}
		_, err := storer.pool.Exec(ctx,
			"INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason) VALUES ($1, $2, $3, $4)", pid, a, b, reason)
		require.NoError(t, err)
	}
	ins(personID, fA, fB, "walked back")   // in-stance: reported under both
	ins(personID, fA, fOut, "outside")     // other fact not in the stance: ignored
	ins(otherID, fB, fC, "foreign person") // not the stance owner's: ignored

	tensions, err := storer.GetStanceTensions(ctx, stanceID)
	require.NoError(t, err)
	require.Len(t, tensions, 2)
	require.Equal(t, []core.StanceTension{{FactID: fA, OtherFactText: "did not-X", Reason: "walked back"}}, tensions[fA])
	require.Equal(t, []core.StanceTension{{FactID: fB, OtherFactText: "promised X", Reason: "walked back"}}, tensions[fB])
	require.Empty(t, tensions[fC])
}
