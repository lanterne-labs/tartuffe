package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecordContradiction_CrossPersonGuard verifies the cross-person guard in
// RecordContradiction: a pair whose two facts share no mentioned person (a
// "different subjects" false positive, e.g. a Lemoine fact vs a Darmanin fact) is
// skipped without recording, while a normal same-person contradiction is recorded.
func TestRecordContradiction_CrossPersonGuard(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	mkPerson := func(name string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO persons (name, entity_type) VALUES ($1, 'human') RETURNING id`, name,
		).Scan(&id))
		return id
	}
	personA := mkPerson("xperson guard a")
	personB := mkPerson("xperson guard b")

	// insertFact creates a fact + its source + a mention linking it to personID.
	insertFact := func(text string, personID int64) int64 {
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

	countPair := func(a, b int64) int {
		t.Helper()
		if a > b {
			a, b = b, a
		}
		var n int
		require.NoError(t, storer.pool.QueryRow(ctx,
			`SELECT COUNT(*) FROM contradictions WHERE fact_a_id=$1 AND fact_b_id=$2`, a, b).Scan(&n))
		return n
	}

	// Cross-person pair: factA mentions only personA, factB mentions only personB.
	factA := insertFact("xguard: Patricia Lemoine said she is not a political deputy", personA)
	factB := insertFact("xguard: Gerald Darmanin withdrew the plea-bargain provision", personB)

	cid, _, err := storer.RecordContradictionImmediate(ctx, personA, factA, factB, "different subjects FP", "")
	require.NoError(t, err, "cross-person pair must not error — it should be skipped")
	assert.Equal(t, int64(0), cid, "cross-person pair must not be recorded")
	assert.Equal(t, 0, countPair(factA, factB), "no contradiction row for a cross-person pair")

	// Same-person pair: both facts mention personA.
	factA2 := insertFact("xguard: Person A promised to do X", personA)
	factA3 := insertFact("xguard: Person A did the opposite of X", personA)

	cid2, _, err := storer.RecordContradictionImmediate(ctx, personA, factA2, factA3, "real same-person contradiction", "")
	require.NoError(t, err)
	assert.Greater(t, cid2, int64(0), "same-person contradiction must be recorded")
	assert.Equal(t, 1, countPair(factA2, factA3), "same-person contradiction row exists")
}

// TestRecordContradiction_GroupParent records a contradiction on the parents
// of the claim groups: a member stands for its parent, and two facts of one
// group are no contradiction.
func TestRecordContradiction_GroupParent(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var person int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('group parent person', 'human') RETURNING id`,
	).Scan(&person))
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
			`INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $2 FROM fact_sources WHERE fact_id = $1`,
			id, person)
		require.NoError(t, err)
		return id
	}
	parent := insertFact("grp: reserve Schengen to Europeans (le Figaro)")
	member := insertFact("grp: reserve Schengen to Europeans (X)")
	other := insertFact("grp: leave Schengen")
	_, err := storer.pool.Exec(ctx,
		`INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type, reason) VALUES ($1, $2, 'duplicate', 'same claim')`,
		parent, member)
	require.NoError(t, err)

	cid, _, err := storer.RecordContradictionImmediate(ctx, person, member, other, "reform vs leave", "")
	require.NoError(t, err)
	require.Greater(t, cid, int64(0))
	cid2, _, err := storer.RecordContradictionImmediate(ctx, person, parent, other, "reform vs leave", "")
	require.NoError(t, err)
	assert.Equal(t, cid, cid2, "the member's pair is the parent's pair")
	var a, b int64
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT fact_a_id, fact_b_id FROM contradictions WHERE id = $1`, cid).Scan(&a, &b))
	assert.ElementsMatch(t, []int64{parent, other}, []int64{a, b})

	cid3, _, err := storer.RecordContradictionImmediate(ctx, person, member, parent, "one claim", "")
	require.NoError(t, err)
	assert.Equal(t, int64(0), cid3, "two facts of one group are no contradiction")
}
