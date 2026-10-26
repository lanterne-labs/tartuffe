package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOperationProgress: a progress record keeps done and total under its
// operation, and a collection task keeps the operation it was added under.
func TestOperationProgress(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, storer.RecordProgress(ctx, "progress test op", 12, 40))
	var done, total float64
	require.NoError(t, storer.pool.QueryRow(ctx, `
		SELECT MAX(value) FILTER (WHERE metric = 'done'), MAX(value) FILTER (WHERE metric = 'total')
		FROM job_metrics WHERE job_type = $1 AND target = 'progress test op'`, ProgressJob).Scan(&done, &total))
	assert.Equal(t, 12.0, done)
	assert.Equal(t, 40.0, total)

	_, err := storer.pool.Exec(ctx, "TRUNCATE collection_tasks")
	require.NoError(t, err)
	p, err := storer.FindOrInsertPersonWithType(ctx, "operation person", "human")
	require.NoError(t, err)
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	_, err = storer.AddCollectionTasks(ctx, []NewCollectionTask{
		{PersonID: p, Plugin: "news", Since: day, Until: day.AddDate(0, 0, 30), Operation: "backfill 2y"},
		{PersonID: p, Plugin: "twitter", Since: day, Until: day.AddDate(0, 0, 30)},
	})
	require.NoError(t, err)
	got := map[string]string{}
	for range 2 {
		task, err := storer.ClaimCollectionTask(ctx, time.Hour, 0)
		require.NoError(t, err)
		require.NotNil(t, task)
		got[task.Plugin] = task.Operation
	}
	assert.Equal(t, map[string]string{"news": "backfill 2y", "twitter": ""}, got)
}
