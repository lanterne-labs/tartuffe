package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFactPairQueue pins the generic pair queue and its first producer query:
// a transcript fact and a vote fact of the same person sharing a source
// seance_ref are queued once; already-judged pairs, empty keys, other
// persons' facts and other séances are not; a folded member resolves to its
// meta; consumption takes and deletes oldest-first under a limit; the
// metadata backfill makes older vote sources pairable and is idempotent.
func TestFactPairQueue(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	truncateFanoutTables(t, storer)
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE fact_pair_queue, fact_pair_checks RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personP, _, err := storer.AddPerson(ctx, "Queue Person P")
	require.NoError(t, err)
	personQ, _, err := storer.AddPerson(ctx, "Queue Person Q")
	require.NoError(t, err)

	newFact := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, canonical_verb) VALUES ($1, 'said') RETURNING id", text).Scan(&id))
		return id
	}
	seq := 0
	source := func(factID int64, sourceType string, metadata string, persons ...int64) {
		seq++
		var sid int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO fact_sources (fact_id, source_url, source_type, metadata) VALUES ($1, $2, $3, $4::jsonb) RETURNING id",
			factID, "https://src.test/"+sourceType+"/"+string(rune('a'+seq)), sourceType, metadata).Scan(&sid))
		for _, p := range persons {
			_, err := storer.pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sid, p)
			require.NoError(t, err)
		}
	}
	ordered := func(a, b int64) (int64, int64) {
		if a < b {
			return a, b
		}
		return b, a
	}

	d1 := newFact("a dit en séance S1")
	source(d1, "assemblee_debat", `{"seance_ref":"S1","cr_uid":"CR1"}`, personP)
	v1 := newFact("a voté POUR en séance S1")
	source(v1, "assemblee_law_page", `{"seance_ref":"S1","law_key":"17-1"}`, personP, personQ)
	v2 := newFact("a voté CONTRE en séance S1, déjà jugé")
	source(v2, "assemblee_law_page", `{"seance_ref":"S1","law_key":"17-2"}`, personP)
	v3 := newFact("a voté en séance S2")
	source(v3, "assemblee_law_page", `{"seance_ref":"S2","law_key":"17-3"}`, personP)
	vOld := newFact("a voté, source sans seance_ref")
	source(vOld, "assemblee_law_page", `{"law_key":"17-4"}`, personP)
	vEmpty := newFact("a voté, seance_ref vide")
	source(vEmpty, "assemblee_law_page", `{"seance_ref":"","law_key":"17-5"}`, personP)
	dEmpty := newFact("a dit, seance_ref vide")
	source(dEmpty, "assemblee_debat", `{"seance_ref":"","cr_uid":"CR0"}`, personP)
	// A folded member (transcript restated → meta d1): its source must pair
	// through d1, not through the member.
	member := newFact("a dit en séance S3 (membre replié sous d1)")
	source(member, "assemblee_debat", `{"seance_ref":"S3","cr_uid":"CR3"}`, personP)
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')", d1, member)
	require.NoError(t, err)
	v4 := newFact("a voté en séance S3")
	source(v4, "assemblee_law_page", `{"seance_ref":"S3","law_key":"17-6"}`, personP)

	// (d1, v2) was judged before: never re-queued.
	require.NoError(t, storer.RecordPairChecksBatch(ctx, personP, "h", []PairCheckResult{{FactAID: d1, FactBID: v2, IsContradiction: false}}))

	n, err := storer.EnqueueFactPairsBySharedSourceKey(ctx, "assemblee_debat", "assemblee_law_page", "seance_ref", "test:same-seance")
	require.NoError(t, err)
	require.Equal(t, 2, n, "(d1, v1) on S1 and (d1, v4) via the folded member on S3; v2 judged, v3 other séance, vOld no key, empty keys never pair, Q has no transcript")

	total, err := storer.CountQueuedPairs(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, total)

	// Idempotent.
	n, err = storer.EnqueueFactPairsBySharedSourceKey(ctx, "assemblee_debat", "assemblee_law_page", "seance_ref", "test:same-seance")
	require.NoError(t, err)
	require.Equal(t, 0, n)

	// Backfilling seance_ref on the old vote source makes it pairable; a
	// second backfill is a no-op.
	updated, err := storer.SetFactSourceMetadataByKey(ctx, "assemblee_law_page", "law_key", "seance_ref", map[string]string{"17-4": "S1", "17-999": "S9"})
	require.NoError(t, err)
	require.Equal(t, 1, updated)
	updated, err = storer.SetFactSourceMetadataByKey(ctx, "assemblee_law_page", "law_key", "seance_ref", map[string]string{"17-4": "S1"})
	require.NoError(t, err)
	require.Equal(t, 0, updated)
	n, err = storer.EnqueueFactPairsBySharedSourceKey(ctx, "assemblee_debat", "assemblee_law_page", "seance_ref", "test:same-seance")
	require.NoError(t, err)
	require.Equal(t, 1, n, "the backfilled vote now pairs with the transcript")

	// Consumption: oldest first, bounded, deleted as taken.
	taken, err := storer.ConsumeQueuedPairsForPerson(ctx, personP, 2)
	require.NoError(t, err)
	require.Len(t, taken, 2)
	a1, b1 := ordered(d1, v1)
	require.Equal(t, QueuedFactPair{FactAID: a1, FactBID: b1, Source: "test:same-seance"}, taken[0])
	total, err = storer.CountQueuedPairs(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, total, "one pair left for the next night")
	rest, err := storer.ConsumeQueuedPairsForPerson(ctx, personP, 10)
	require.NoError(t, err)
	require.Len(t, rest, 1)
	aOld, bOld := ordered(d1, vOld)
	require.Equal(t, aOld, rest[0].FactAID)
	require.Equal(t, bOld, rest[0].FactBID)
	none, err := storer.ConsumeQueuedPairsForPerson(ctx, personQ, 10)
	require.NoError(t, err)
	require.Empty(t, none)
	total, err = storer.CountQueuedPairs(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, total)
}
