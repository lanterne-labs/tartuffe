package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestEvictStaleJudgedPairs pins the re-judge primitive: negative verdicts
// whose judge_hash is not the current one (NULL included) are evicted oldest
// first under the limit; negatives already carrying the current hash and
// every positive verdict are untouchable; the count of stale negatives is
// reported so the step is self-terminating and observable.
func TestEvictStaleJudgedPairs(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE fact_pair_checks RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personID, _, err := storer.AddPerson(ctx, "Rejudge Fixture")
	require.NoError(t, err)
	var factIDs []int64
	for i := 0; i < 5; i++ {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text) VALUES ($1) RETURNING id", "fixture").Scan(&id))
		factIDs = append(factIDs, id)
	}
	now := time.Now()
	ins := func(a, b int64, contra bool, hash *string, at time.Time) {
		_, err := storer.pool.Exec(ctx,
			"INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction, judge_hash, checked_at) VALUES ($1,$2,$3,$4,$5,$6)",
			personID, a, b, contra, hash, at)
		require.NoError(t, err)
	}
	old, cur := "oldhash", "curhash"
	ins(factIDs[0], factIDs[1], false, nil, now.Add(-72*time.Hour))  // never hashed, oldest
	ins(factIDs[0], factIDs[2], false, &old, now.Add(-48*time.Hour)) // stale rulebook
	ins(factIDs[0], factIDs[3], false, &old, now.Add(-24*time.Hour)) // stale rulebook
	ins(factIDs[1], factIDs[2], true, &old, now.Add(-24*time.Hour))  // positive — auditor's job
	ins(factIDs[1], factIDs[3], false, &cur, now.Add(-time.Hour))    // current rulebook — final
	ins(factIDs[2], factIDs[4], false, nil, now)                     // never hashed, newest

	deleted, remaining, err := storer.EvictStaleJudgedPairs(ctx, cur, 2)
	require.NoError(t, err)
	require.Equal(t, 2, deleted, "limit honored, oldest first")
	require.Equal(t, 2, remaining, "two stale negatives left")

	var left []int64
	rows, err := storer.pool.Query(ctx, "SELECT fact_b_id FROM fact_pair_checks WHERE NOT is_contradiction ORDER BY checked_at")
	require.NoError(t, err)
	for rows.Next() {
		var b int64
		require.NoError(t, rows.Scan(&b))
		left = append(left, b)
	}
	rows.Close()
	require.Equal(t, []int64{factIDs[3], factIDs[3], factIDs[4]}, left, "the two oldest stale negatives are gone")

	deleted, remaining, err = storer.EvictStaleJudgedPairs(ctx, cur, 1500)
	require.NoError(t, err)
	require.Equal(t, 2, deleted)
	require.Equal(t, 0, remaining, "self-terminating: every negative carries the current hash")

	var contras, currentNeg int
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT count(*) FROM fact_pair_checks WHERE is_contradiction").Scan(&contras))
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT count(*) FROM fact_pair_checks WHERE NOT is_contradiction").Scan(&currentNeg))
	require.Equal(t, 1, contras, "positive verdicts never evicted")
	require.Equal(t, 1, currentNeg, "current-rulebook negatives never evicted")

	// Recording stamps the hash, and re-recording a re-judged pair replaces it.
	require.NoError(t, storer.RecordPairChecksBatch(ctx, personID, cur, []PairCheckResult{{FactAID: factIDs[0], FactBID: factIDs[1], IsContradiction: false}}))
	var h string
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT judge_hash FROM fact_pair_checks WHERE fact_a_id=$1 AND fact_b_id=$2", factIDs[0], factIDs[1]).Scan(&h))
	require.Equal(t, cur, h)
	_, remaining, err = storer.EvictStaleJudgedPairs(ctx, cur, 1500)
	require.NoError(t, err)
	require.Equal(t, 0, remaining)
}
