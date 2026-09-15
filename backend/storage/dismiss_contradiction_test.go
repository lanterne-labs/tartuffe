package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDismissContradictionAsFalsePositive verifies the auditor-apply path:
// dismissing a contradiction deletes the row, records a NEGATIVE fact_pair_checks
// row (so the detector won't re-raise the pair), and recomputes the honesty score.
// This contrasts with DeleteContradiction, which CLEARS the pair check.
func TestDismissContradictionAsFalsePositive(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('dismiss person', 'human') RETURNING id`,
	).Scan(&personID))

	insertFact := func(text string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id`,
			text, pgvector.NewVector(mockEmbedding()),
		).Scan(&id))
		_, err := storer.pool.Exec(ctx,
			`INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type) VALUES ($1, $2, $3, 'test')`,
			id, fmt.Sprintf("http://ex/%d", id), text)
		require.NoError(t, err)
		_, err = storer.pool.Exec(ctx,
			`INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $2 FROM fact_sources WHERE fact_id = $1 ORDER BY id DESC LIMIT 1`,
			id, personID)
		require.NoError(t, err)
		return id
	}

	fa := insertFact("A: claimed never raised taxes")
	fb := insertFact("B: announced a specific local fee — not actually a contradiction")

	cid, _, err := storer.RecordContradictionImmediate(ctx, personID, fa, fb, "false-positive pair", "")
	require.NoError(t, err)

	// Recording a contradiction depresses the honesty score below 1.
	var scoreWithContra float64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT honesty_score FROM persons WHERE id=$1`, personID).Scan(&scoreWithContra))
	assert.Less(t, scoreWithContra, 1.0, "contradiction should depress the score")

	// Dismiss as a false positive.
	require.NoError(t, storer.DismissContradictionAsFalsePositive(ctx, cid))

	// 1. Contradiction row is gone.
	var remaining int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM contradictions WHERE id=$1`, cid).Scan(&remaining))
	assert.Equal(t, 0, remaining, "contradiction should be deleted")

	// 2. A NEGATIVE pair check exists (canonical ordering fact_a < fact_b), so the
	//    detector treats the pair as already-judged-not-contradictory.
	a, b := fa, fb
	if a > b {
		a, b = b, a
	}
	var isContra *bool
	err = storer.pool.QueryRow(ctx,
		`SELECT is_contradiction FROM fact_pair_checks WHERE person_id=$1 AND fact_a_id=$2 AND fact_b_id=$3`,
		personID, a, b).Scan(&isContra)
	require.NoError(t, err, "a negative pair check row must exist")
	require.NotNil(t, isContra)
	assert.False(t, *isContra, "pair must be recorded as NOT a contradiction")

	// 3. The pair is in the checked set the detector uses to skip re-judging.
	checked, err := storer.GetCheckedPairs(ctx, personID)
	require.NoError(t, err)
	assert.Contains(t, checked, fmt.Sprintf("%d-%d", a, b), "detector should see the pair as already checked")

	// 4. Honesty score recovered (no contradictions left).
	var scoreAfter float64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT honesty_score FROM persons WHERE id=$1`, personID).Scan(&scoreAfter))
	assert.Greater(t, scoreAfter, scoreWithContra, "dismissing the FP should raise the score back")
}

// TestDeleteContradictionClearsPairCheck is the contrast case: DeleteContradiction
// (the self-correcting path) must NOT leave a pair check behind, so the pair can be
// re-analyzed on a later detector run.
func TestDeleteContradictionClearsPairCheck(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('delete person', 'human') RETURNING id`,
	).Scan(&personID))

	insertFact := func(text string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id`,
			text, pgvector.NewVector(mockEmbedding()),
		).Scan(&id))
		_, err := storer.pool.Exec(ctx,
			`INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type) VALUES ($1, $2, $3, 'test')`,
			id, fmt.Sprintf("http://ex/%d", id), text)
		require.NoError(t, err)
		_, err = storer.pool.Exec(ctx,
			`INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $2 FROM fact_sources WHERE fact_id = $1 ORDER BY id DESC LIMIT 1`,
			id, personID)
		require.NoError(t, err)
		return id
	}

	fa := insertFact("A: position X")
	fb := insertFact("B: opposing position not-X")

	cid, _, err := storer.RecordContradictionImmediate(ctx, personID, fa, fb, "real contradiction", "")
	require.NoError(t, err)

	a, b := fa, fb
	if a > b {
		a, b = b, a
	}
	// Seed a positive pair check so the clear is actually exercised.
	require.NoError(t, storer.RecordPairChecksBatch(ctx, personID, "testhash",
		[]PairCheckResult{{FactAID: a, FactBID: b, IsContradiction: true}}))

	require.NoError(t, storer.DeleteContradiction(ctx, cid))

	var n int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM fact_pair_checks WHERE person_id=$1 AND fact_a_id=$2 AND fact_b_id=$3`,
		personID, a, b).Scan(&n))
	assert.Equal(t, 0, n, "DeleteContradiction must clear the pair check so the pair can be re-analyzed")
}
