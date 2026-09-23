package storage

import (
	"context"
	"digital-mirror/backend/core"
	"fmt"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func day(now time.Time, daysAgo int) time.Time {
	return now.AddDate(0, 0, -daysAgo)
}

// insertDatedFact stores a fact with one source (published_at nil = dateless) mentioning
// personID, going through linkPersonsToSourceMention so the score-refresh path runs as in production.
func insertDatedFact(t *testing.T, storer *PostgresStorer, personID int64, text string, publishedAt *time.Time) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id`,
		text, pgvector.NewVector(mockEmbedding()),
	).Scan(&id))
	var sourceID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type, published_at) VALUES ($1, $2, $3, 'test', $4) RETURNING id`,
		id, fmt.Sprintf("http://ex/%d", id), text, publishedAt,
	).Scan(&sourceID))
	tx, err := storer.pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, storer.linkPersonsToSourceMention(ctx, tx, sourceID, []int64{personID}))
	require.NoError(t, tx.Commit(ctx))
	return id
}

// TestBuildPersonHistory_CumulativeAndRecentDiverge checks the replay on a fixed clock:
// an old contradiction stays in the cumulative score forever but leaves the trailing
// window, so RecentScore recovers while Score does not.
func TestBuildPersonHistory_CumulativeAndRecentDiverge(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	events := []historyEvent{
		{at: day(now, 10)},                      // recent fact
		{at: day(now, 490), bundle: "stance:1"}, // old contradiction (later fact at -490d)
		{at: day(now, 500)},                     // old fact
		{at: day(now, 490)},                     // old fact
		{at: now.AddDate(0, 0, 30)},             // future-dated fact, clamped to now
	}

	points := buildPersonHistory(events, now)
	require.NotEmpty(t, points)

	first := points[0]
	assert.Equal(t, weekStart(day(now, 500)), first.Date)
	assert.Equal(t, 1, first.FactsAdded)
	assert.Equal(t, 1, first.FactsTotal)
	assert.Equal(t, 0, first.Contradictions)
	assert.Equal(t, 1.0, first.Score)

	// Consecutive weekly buckets up to the current week.
	for i := 1; i < len(points); i++ {
		assert.Equal(t, points[i-1].Date.AddDate(0, 0, 7), points[i].Date)
	}
	last := points[len(points)-1]
	assert.Equal(t, weekStart(now), last.Date)
	assert.Equal(t, 4, last.FactsTotal, "future-dated fact folds into the current week")
	assert.Equal(t, 1, last.Contradictions)
	assert.Equal(t, core.ComputeHonestyScore(4, core.ContradictionStats{Bundles: 1, Total: 1}), last.Score)
	assert.Equal(t, 1.0, last.RecentScore, "old contradiction and facts left the trailing window")

	// At the contradiction's week both series carry it.
	var atContra *HistoryPoint
	for i := range points {
		if points[i].Date.Equal(weekStart(day(now, 490))) {
			atContra = &points[i]
		}
	}
	require.NotNil(t, atContra)
	assert.Equal(t, 2, atContra.FactsTotal)
	assert.Equal(t, 1, atContra.Contradictions)
	assert.Equal(t, atContra.Score, atContra.RecentScore)
	assert.Less(t, atContra.Score, 1.0)
}

func TestBuildPersonHistory_OldEventsFoldIntoFirstPoint(t *testing.T) {
	now := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	events := []historyEvent{
		{at: time.Date(1908, 3, 21, 0, 0, 0, 0, time.UTC)},
		{at: day(now, 3)},
	}
	points := buildPersonHistory(events, now)
	require.NotEmpty(t, points)
	assert.Equal(t, weekStart(now.Add(-personHistoryMaxSpan)), points[0].Date)
	assert.Equal(t, 1, points[0].FactsTotal, "ancient fact counted in the first bucket")
	assert.Equal(t, 2, points[len(points)-1].FactsTotal)
}

func TestBuildPersonHistory_Empty(t *testing.T) {
	assert.Equal(t, []HistoryPoint{}, buildPersonHistory(nil, time.Now()))
}

// TestGetPersonHistory runs the SQL side: facts are dated by their earliest source
// publication, a contradiction by its later fact, and the final point matches the
// live own_score persisted by RecordContradictionImmediate.
func TestGetPersonHistory(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('history person', 'human') RETURNING id`,
	).Scan(&personID))

	insertFact := func(text string, publishedAt time.Time) int64 {
		t.Helper()
		return insertDatedFact(t, storer, personID, text, &publishedAt)
	}

	now := time.Now().UTC()
	fa := insertFact("H: promised to lower the retirement age", day(now, 200))
	fb := insertFact("H: voted to raise the retirement age", day(now, 100))

	_, _, err := storer.RecordContradictionImmediate(ctx, personID, fa, fb, "flip on retirement age", "")
	require.NoError(t, err)

	// A fact ingested after the contradiction must move own_score too (the mention insert
	// refreshes scores), otherwise the chart's last point and the hero gauge disagree.
	insertFact("H: unrelated statement", day(now, 20))

	points, err := storer.GetPersonHistory(ctx, personID)
	require.NoError(t, err)
	require.NotEmpty(t, points)

	assert.Equal(t, weekStart(day(now, 200)), points[0].Date)
	assert.Equal(t, 1, points[0].FactsTotal)
	assert.Equal(t, 0, points[0].Contradictions)

	// The contradiction lands in the week of its LATER fact (fb, -100d), not fa's week.
	for _, p := range points {
		if p.Date.Before(weekStart(day(now, 100))) {
			assert.Equal(t, 0, p.Contradictions, "no contradiction before fb is stated (week %s)", p.Date)
		} else {
			assert.Equal(t, 1, p.Contradictions, "contradiction present from fb's week on (week %s)", p.Date)
		}
	}

	last := points[len(points)-1]
	assert.Equal(t, 3, last.FactsTotal)
	var ownScore float64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT own_score FROM persons WHERE id=$1`, personID).Scan(&ownScore))
	assert.InDelta(t, ownScore, last.Score, 1e-9, "last point must equal the live own_score")

	// No facts at all: empty series, not an error.
	var emptyID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('history nobody', 'human') RETURNING id`,
	).Scan(&emptyID))
	empty, err := storer.GetPersonHistory(ctx, emptyID)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestGetPersonHistory_GroupDatedByMembers: a claim group whose parent is undated
// takes the earliest date among its members, and the members themselves
// (duplicate-marked) must not appear as separate facts.
func TestGetPersonHistory_GroupDatedByMembers(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('group history person', 'human') RETURNING id`,
	).Scan(&personID))

	now := time.Now().UTC()
	older := day(now, 300)
	newer := day(now, 150)
	m1 := insertDatedFact(t, storer, personID, "M: member one", &newer)
	m2 := insertDatedFact(t, storer, personID, "M: member two", &older)
	parent := insertDatedFact(t, storer, personID, "M: undated parent", nil)
	for _, member := range []int64{m1, m2} {
		_, err := storer.pool.Exec(ctx,
			`INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')`,
			parent, member)
		require.NoError(t, err)
	}

	points, err := storer.GetPersonHistory(ctx, personID)
	require.NoError(t, err)
	require.NotEmpty(t, points)

	assert.Equal(t, weekStart(older), points[0].Date, "group dated by its oldest member")
	assert.Equal(t, 1, points[0].FactsTotal)
	last := points[len(points)-1]
	assert.Equal(t, 1, last.FactsTotal, "folded members do not count as separate facts")
}

// TestGetPersonHistory_Cache: a second call with unchanged counts is served from the memo
// (an edit invisible to the fingerprint is not reflected), while a new fact moves the
// fingerprint and forces a recompute.
func TestGetPersonHistory_Cache(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('cached history person', 'human') RETURNING id`,
	).Scan(&personID))

	now := time.Now().UTC()
	first := day(now, 60)
	insertDatedFact(t, storer, personID, "C: first statement", &first)

	points, err := storer.GetPersonHistory(ctx, personID)
	require.NoError(t, err)
	require.Equal(t, weekStart(first), points[0].Date)

	// Fingerprint-invisible edit: the cached series still starts at the old date.
	_, err = storer.pool.Exec(ctx, `UPDATE fact_sources SET published_at = $1`, day(now, 90))
	require.NoError(t, err)
	cached, err := storer.GetPersonHistory(ctx, personID)
	require.NoError(t, err)
	assert.Equal(t, weekStart(first), cached[0].Date, "unchanged fingerprint serves the memo")

	// A new fact changes fact_count and own_score: recompute picks up both changes.
	second := day(now, 10)
	insertDatedFact(t, storer, personID, "C: second statement", &second)
	fresh, err := storer.GetPersonHistory(ctx, personID)
	require.NoError(t, err)
	assert.Equal(t, weekStart(day(now, 90)), fresh[0].Date, "fingerprint change forces a recompute")
	assert.Equal(t, 2, fresh[len(fresh)-1].FactsTotal)
}
