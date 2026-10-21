package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestContradictionReviews(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var person int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('review person', 'human') RETURNING id`,
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
	a, b, c := insertFact("rev: for the pension reform"), insertFact("rev: against the pension reform"), insertFact("rev: for the 49.3")
	first, _, err := storer.RecordContradictionImmediate(ctx, person, a, b, "for vs against", "")
	require.NoError(t, err)
	second, _, err := storer.RecordContradictionImmediate(ctx, person, b, c, "against vs 49.3", "")
	require.NoError(t, err)

	before, err := storer.QualityMetrics(ctx)
	require.NoError(t, err)

	todo, err := storer.ListContradictionsToReview(ctx, "alice", time.Time{}, &person, 0)
	require.NoError(t, err)
	assert.Len(t, todo, 2)

	require.NoError(t, storer.RecordContradictionReview(ctx, "alice", ContradictionReview{ContradictionID: first, Verdict: "debatable"}))
	require.NoError(t, storer.RecordContradictionReview(ctx, "alice", ContradictionReview{ContradictionID: first, Verdict: "real", Note: "clear reversal"}))
	assert.Error(t, storer.RecordContradictionReview(ctx, "alice", ContradictionReview{ContradictionID: second, Verdict: "maybe"}), "unknown verdict")
	assert.Error(t, storer.RecordContradictionReview(ctx, "", ContradictionReview{ContradictionID: second, Verdict: "real"}), "no reviewer")
	assert.Error(t, storer.RecordContradictionReview(ctx, "alice", ContradictionReview{ContradictionID: -1, Verdict: "real"}), "no such contradiction")

	todo, err = storer.ListContradictionsToReview(ctx, "alice", time.Time{}, &person, 0)
	require.NoError(t, err)
	require.Len(t, todo, 1)
	assert.Equal(t, second, todo[0].ID)
	todo, err = storer.ListContradictionsToReview(ctx, "bob", time.Time{}, &person, 0)
	require.NoError(t, err)
	assert.Len(t, todo, 2, "another reviewer has both to review")
	todo, err = storer.ListContradictionsToReview(ctx, "bob", time.Now().Add(time.Hour), &person, 0)
	require.NoError(t, err)
	assert.Empty(t, todo, "nothing detected after since")

	var verdict, note, factA, name string
	var count int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT COUNT(*), MAX(verdict), MAX(note), MAX(fact_a_text), MAX(person_name) FROM contradiction_reviews WHERE contradiction_id = $1`, first,
	).Scan(&count, &verdict, &note, &factA, &name))
	assert.Equal(t, 1, count, "a second review by the same reviewer replaces the first")
	assert.Equal(t, "real", verdict)
	assert.Equal(t, "clear reversal", note)
	assert.Contains(t, []string{"rev: for the pension reform", "rev: against the pension reform"}, factA)
	assert.Equal(t, "review person", name)

	// The review outlives its contradiction.
	_, err = storer.pool.Exec(ctx, `DELETE FROM contradictions WHERE id = $1`, first)
	require.NoError(t, err)
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM contradiction_reviews WHERE contradiction_id IS NULL AND person_id = $1 AND verdict = 'real'`, person,
	).Scan(&count))
	assert.Equal(t, 1, count)

	after, err := storer.QualityMetrics(ctx)
	require.NoError(t, err)
	byName := func(rows []JobMetric) map[string]float64 {
		m := map[string]float64{}
		for _, r := range rows {
			assert.Equal(t, QualityJob, r.JobType)
			m[r.Metric] = r.Value
		}
		return m
	}
	assert.Equal(t, byName(before)["contradictions"]-1, byName(after)["contradictions"])
	assert.Equal(t, byName(before)["facts"], byName(after)["facts"])

	recorded, err := storer.RecordQualityMetrics(ctx)
	require.NoError(t, err)
	at, value, ok, err := storer.LastJobMetric(ctx, QualityJob, "contradictions")
	require.NoError(t, err)
	require.True(t, ok)
	assert.WithinDuration(t, time.Now(), at, time.Minute)
	assert.Equal(t, byName(recorded)["contradictions"], value)
}
