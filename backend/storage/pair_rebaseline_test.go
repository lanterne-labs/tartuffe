package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEvictStalePairChecks(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE fact_pair_checks RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	storer.SetEntityValidator(nil)

	personID, _, err := storer.AddPerson(ctx, "Rebaseline Fixture")
	require.NoError(t, err)
	var factIDs []int64
	for i := 0; i < 4; i++ {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text) VALUES ($1) RETURNING id", "fixture").Scan(&id))
		factIDs = append(factIDs, id)
	}
	cutoff := time.Date(2026, 9, 5, 18, 0, 0, 0, time.UTC)
	// three legacy NO rows at increasing ages, one legacy YES, one fresh NO
	ins := func(a, b int64, contra bool, at time.Time) {
		_, err := storer.pool.Exec(ctx,
			"INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction, checked_at) VALUES ($1,$2,$3,$4,$5)",
			personID, a, b, contra, at)
		require.NoError(t, err)
	}
	ins(factIDs[0], factIDs[1], false, cutoff.Add(-72*time.Hour)) // oldest legacy NO
	ins(factIDs[0], factIDs[2], false, cutoff.Add(-48*time.Hour)) // legacy NO
	ins(factIDs[0], factIDs[3], false, cutoff.Add(-24*time.Hour)) // legacy NO
	ins(factIDs[1], factIDs[2], true, cutoff.Add(-24*time.Hour))  // legacy YES — untouchable
	ins(factIDs[1], factIDs[3], false, cutoff.Add(time.Hour))     // fresh NO — untouchable

	deleted, remaining, err := storer.EvictStalePairChecks(ctx, cutoff, 2)
	require.NoError(t, err)
	require.Equal(t, 2, deleted, "limit honored, oldest first")
	require.Equal(t, 1, remaining, "one legacy NO left")

	deleted, remaining, err = storer.EvictStalePairChecks(ctx, cutoff, 1500)
	require.NoError(t, err)
	require.Equal(t, 1, deleted)
	require.Equal(t, 0, remaining, "self-terminating: nothing predates the cutoff")

	var contras, fresh int
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT count(*) FROM fact_pair_checks WHERE is_contradiction").Scan(&contras))
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT count(*) FROM fact_pair_checks WHERE NOT is_contradiction").Scan(&fresh))
	require.Equal(t, 1, contras, "recorded contradictions never evicted")
	require.Equal(t, 1, fresh, "post-cutoff verdicts never evicted")
}
