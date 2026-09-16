package storage

import (
	"context"
	"digital-mirror/backend/core"
	"fmt"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStarPeel verifies the pure greedy star-peeling that turns pairwise edges into
// directional base -> spoke assignments. No DB needed.
func TestStarPeel(t *testing.T) {
	// Helper: build rows with explicit ids so assertions are stable.
	row := func(id, a, b int64) regroupRow { return regroupRow{id: id, a: a, b: b} }

	t.Run("star collapses to the shared base", func(t *testing.T) {
		// 10 is contradicted by 11,12,13. 10 has degree 3 -> base for all.
		rows := []regroupRow{row(1, 11, 10), row(2, 12, 10), row(3, 13, 10)}
		base, other := starPeel(rows)
		for _, r := range rows {
			assert.Equal(t, int64(10), base[r.id], "row %d base", r.id)
		}
		assert.ElementsMatch(t, []int64{11, 12, 13}, []int64{other[1], other[2], other[3]})
	})

	t.Run("triangle keeps the leftover edge as its own group", func(t *testing.T) {
		// Edges 1-2,1-3,2-3 all degree 2 -> base=1 (lowest id) absorbs 1-2,1-3;
		// leftover 2-3 becomes its own group with base=2.
		rows := []regroupRow{row(1, 1, 2), row(2, 1, 3), row(3, 2, 3)}
		base, _ := starPeel(rows)
		assert.Equal(t, int64(1), base[1])
		assert.Equal(t, int64(1), base[2])
		assert.Equal(t, int64(2), base[3], "leftover 2-3 gets its own base")
	})

	t.Run("disjoint components are independent", func(t *testing.T) {
		rows := []regroupRow{row(1, 1, 2), row(2, 1, 3), row(3, 5, 6), row(4, 5, 7)}
		base, _ := starPeel(rows)
		assert.Equal(t, int64(1), base[1])
		assert.Equal(t, int64(1), base[2])
		assert.Equal(t, int64(5), base[3])
		assert.Equal(t, int64(5), base[4])
	})
}

// TestRegroupPersonContradictions exercises the full DB-backed regroup: a base fact
// contradicted by three facts, then asserts every row's base_fact_id points at the
// base, the merged summary is persisted, and the grouped read returns a single card
// with three spokes. The canonical (fact_a_id, fact_b_id) pair is left untouched.
func TestRegroupPersonContradictions(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('regroup person', 'human') RETURNING id`,
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

	// Spokes first (small ids), base last (large id) so canonical-min insert makes
	// the base fact_b and the regroup pass must flip every row.
	s1 := insertFact("S1: revenue source A")
	s2 := insertFact("S2: revenue source B")
	s3 := insertFact("S3: revenue source C")
	base := insertFact("BASE: promised aid without a tax increase")

	stance, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID: personID, ClusterLabel: "Fuel aid financing",
		ClusterEmbedding: mockEmbedding(),
		Summary:          "How the promised aid is financed.",
		KeyFactIDs:       []int64{base}, FactCount: 4,
	})
	require.NoError(t, err)
	for _, fid := range []int64{s1, s2, s3, base} {
		require.NoError(t, storer.AddFactToStance(ctx, stance, fid))
	}

	for _, s := range []int64{s1, s2, s3} {
		_, _, err := storer.RecordContradictionImmediate(ctx, personID, base, s, fmt.Sprintf("base vs %d", s), "")
		require.NoError(t, err)
	}

	// Fake summarizer: records the call and returns canned text (no LLM).
	var gotBase string
	var gotSpokes int
	summarize := func(_ context.Context, baseFact string, spokes []GroupSpokeInput) (string, string, error) {
		gotBase = baseFact
		gotSpokes = len(spokes)
		return "merged: aid promise conflicts with three revenue facts", "merged-en", nil
	}

	res, err := storer.RegroupPersonContradictions(ctx, personID, summarize)
	require.NoError(t, err)
	assert.Equal(t, 3, res.Rebased, "all three rows pointed at the base")
	assert.Equal(t, 1, res.Groups, "one multi-fact group")
	assert.Equal(t, 1, res.SummariesWrote)
	assert.Equal(t, "BASE: promised aid without a tax increase", gotBase)
	assert.Equal(t, 3, gotSpokes)

	// All three rows now have base_fact_id = base (canonical pair untouched).
	var baseRows int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM contradictions WHERE person_id=$1 AND base_fact_id=$2`, personID, base,
	).Scan(&baseRows))
	assert.Equal(t, 3, baseRows)

	// Grouped read returns one card: base + 3 spokes + persisted summary.
	groups, err := storer.GetStanceContradictionGroups(ctx, stance)
	require.NoError(t, err)
	require.Len(t, groups, 1, "one inconsistency card")
	g := groups[0]
	assert.Equal(t, base, g.BaseFactID)
	assert.Len(t, g.ContradictingFacts, 3)
	require.NotNil(t, g.Summary)
	assert.Equal(t, "merged: aid promise conflicts with three revenue facts", *g.Summary)

	// Idempotent: a second regroup makes no base changes.
	res2, err := storer.RegroupPersonContradictions(ctx, personID, summarize)
	require.NoError(t, err)
	assert.Equal(t, 0, res2.Rebased, "already grouped → no rebasing on re-run")
}
