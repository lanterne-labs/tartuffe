package storage

import (
	"context"
	"testing"

	"digital-mirror/backend/metrics"

	"github.com/stretchr/testify/require"
)

// Counts add up per (stage, outcome) day, and the totals come back split
// all-time vs today.
func TestDecisionCounters(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE decision_counters")
	require.NoError(t, err)

	require.NoError(t, storer.AddDecisionCounts(ctx, []metrics.DecisionCount{
		{Stage: "fact_dedupe", Outcome: "duplicate", Count: 2},
		{Stage: "fact_dedupe", Outcome: "distinct", Count: 5},
	}))
	require.NoError(t, storer.AddDecisionCounts(ctx, []metrics.DecisionCount{
		{Stage: "fact_dedupe", Outcome: "duplicate", Count: 3},
	}))
	require.NoError(t, storer.AddDecisionCounts(ctx, nil))

	// A count from an earlier day is in the all-time total, not in today's.
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO decision_counters (stage, outcome, day, count) VALUES ('fact_dedupe', 'duplicate', CURRENT_DATE - 1, 10)")
	require.NoError(t, err)

	allTime, today, err := storer.DecisionTotals(ctx)
	require.NoError(t, err)
	require.Equal(t, []metrics.DecisionCount{
		{Stage: "fact_dedupe", Outcome: "distinct", Count: 5},
		{Stage: "fact_dedupe", Outcome: "duplicate", Count: 15},
	}, allTime)
	require.Equal(t, []metrics.DecisionCount{
		{Stage: "fact_dedupe", Outcome: "distinct", Count: 5},
		{Stage: "fact_dedupe", Outcome: "duplicate", Count: 5},
	}, today)
}

// Close flushes what the process counted, so a CLI job's judgements reach the
// database even though only the web process is scraped.
func TestCloseFlushesDecisions(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE decision_counters")
	require.NoError(t, err)

	// A second storer over the same database: closing it flushes without
	// tearing down the pool the assertions use.
	closing, err := NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	metrics.RecordDecision("test_stage", "test_outcome")
	closing.Close()

	var n int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		"SELECT count FROM decision_counters WHERE stage = 'test_stage' AND outcome = 'test_outcome' AND day = CURRENT_DATE").Scan(&n))
	require.EqualValues(t, 1, n)
}
