package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPodiumSnapshots: the live podium is the three candidates with the
// lowest reliable score; a snapshot keeps the podium of its week with the
// scores of then, a second one in the same week replaces it, and the past
// weeks are listed latest first. The person route gives the date of the
// latest fact of a contradiction.
func TestPodiumSnapshots(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	person := func(name string, facts int, honesty float64, candidate bool) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO persons (name, entity_type) VALUES ($1, 'human') RETURNING id`, name).Scan(&id))
		if candidate {
			require.NoError(t, storer.SetCandidate(ctx, id, true))
		}
		_, err := storer.pool.Exec(ctx, `UPDATE persons SET fact_count = $2, honesty_score = $3 WHERE id = $1`, id, facts, honesty)
		require.NoError(t, err)
		return id
	}
	a := person("podium a", 1000, 0.3, true)
	// 13 contradictions over the candidates' 4005 facts: a person is covered
	// from 3 / (13 / 4005) = 924 facts.
	now := time.Now().UTC()
	last := now.AddDate(0, 0, -12)
	old := now.AddDate(-2, 0, 0)
	first := insertDatedFact(t, storer, a, "A: said one thing", &old)
	for i := 1; i <= 13; i++ {
		at := old.AddDate(0, 0, i)
		if i == 13 {
			at = last
		}
		other := insertDatedFact(t, storer, a, fmt.Sprintf("A: said the opposite %d", i), &at)
		_, err := storer.pool.Exec(ctx,
			`INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason) VALUES ($1, $2, $3, 't')`, a, first, other)
		require.NoError(t, err)
	}
	_, err := storer.pool.Exec(ctx, `UPDATE persons SET fact_count = 1000, honesty_score = 0.3 WHERE id = $1`, a)
	require.NoError(t, err)
	b := person("podium b", 1000, 0.5, true)
	c := person("podium c", 1000, 0.6, true)
	d := person("podium d", 1000, 0.9, true)
	person("podium clean sheet", 5, 1.0, true) // not covered, above the floor: not reliable
	person("podium not candidate", 1000, 0.1, false)

	ids := func(entries []PodiumEntry) []int64 {
		out := []int64{}
		for i, e := range entries {
			assert.Equal(t, i+1, e.Rank)
			out = append(out, e.ID)
		}
		return out
	}

	live, err := storer.LivePodium(ctx)
	require.NoError(t, err)
	assert.Equal(t, []int64{a, b, c}, ids(live))
	assert.Equal(t, "podium-a", live[0].Slug)
	assert.Equal(t, 1000, live[0].FactCount)

	// The podium of last week, then of this week after d's score fell.
	lastWeek := now.AddDate(0, 0, -7)
	n, err := storer.RecordPodiumSnapshot(ctx, lastWeek)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	_, err = storer.RecordPodiumSnapshot(ctx, now)
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx, `UPDATE persons SET honesty_score = 0.1 WHERE id = $1`, d)
	require.NoError(t, err)
	_, err = storer.RecordPodiumSnapshot(ctx, now)
	require.NoError(t, err)

	this, err := storer.PodiumSnapshot(ctx, WeekStart(now))
	require.NoError(t, err)
	assert.Equal(t, []int64{d, a, b}, ids(this), "the second snapshot of a week replaces the first")

	past, err := storer.PodiumSnapshot(ctx, WeekStart(lastWeek))
	require.NoError(t, err)
	assert.Equal(t, []int64{a, b, c}, ids(past))
	assert.InDelta(t, 0.3, past[0].HonestyScore, 1e-9)

	weeks, err := storer.PodiumWeeks(ctx, WeekStart(now))
	require.NoError(t, err)
	require.Len(t, weeks, 1)
	assert.Equal(t, WeekStart(lastWeek).Format(time.DateOnly), weeks[0].Format(time.DateOnly))

	none, err := storer.PodiumSnapshot(ctx, WeekStart(now.AddDate(0, 0, -70)))
	require.NoError(t, err)
	assert.Empty(t, none)

	// The latest fact of a contradiction, on the person route.
	pa, err := storer.GetPersonByID(ctx, a)
	require.NoError(t, err)
	require.NotNil(t, pa.LastContradictionAt)
	assert.WithinDuration(t, last, *pa.LastContradictionAt, time.Second)
	assert.Nil(t, pa.LastContradictionPrecision, "a source's date is a full date")
	pb, err := storer.GetPersonByID(ctx, b)
	require.NoError(t, err)
	assert.Nil(t, pb.LastContradictionAt, "no contradiction")
}
