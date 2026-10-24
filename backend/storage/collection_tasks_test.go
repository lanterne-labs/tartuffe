package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCollectionTasksClaimOrderAndLease: the claim takes the highest priority
// first, then the oldest; a released task waits for its not_before; a running
// task whose heartbeat is older than the lease is claimed again; a finished or
// cancelled task is not.
func TestCollectionTasksClaimOrderAndLease(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE collection_tasks")
	require.NoError(t, err)
	a, err := storer.FindOrInsertPersonWithType(ctx, "task person a", "human")
	require.NoError(t, err)
	b, err := storer.FindOrInsertPersonWithType(ctx, "task person b", "party")
	require.NoError(t, err)
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	added, err := storer.AddCollectionTasks(ctx, []NewCollectionTask{
		{PersonID: a, Plugin: "news", Since: day, Until: day.AddDate(0, 0, 30)},
		{PersonID: b, Plugin: "twitter", Since: day, Until: day.AddDate(0, 0, 30), Priority: 5},
		{PersonID: a, Plugin: "news", Since: day, Until: day.AddDate(0, 0, 30)}, // open twice: not added
	})
	require.NoError(t, err)
	assert.Equal(t, 2, added)
	_, err = storer.AddCollectionTasks(ctx, []NewCollectionTask{{PersonID: a, Plugin: "news", Since: day, Until: day}})
	require.Error(t, err, "an empty period is refused")

	first, err := storer.ClaimCollectionTask(ctx, time.Hour)
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, "twitter", first.Plugin, "the higher priority first")
	assert.Equal(t, "party", first.EntityType)
	assert.Equal(t, TaskRunning, first.Status)
	assert.Equal(t, 1, first.Attempts)

	second, err := storer.ClaimCollectionTask(ctx, time.Hour)
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, "news", second.Plugin)
	none, err := storer.ClaimCollectionTask(ctx, time.Hour)
	require.NoError(t, err)
	assert.Nil(t, none, "both tasks are running within their lease")

	// The lease of the first expired: it is claimed again, with one more attempt.
	_, err = storer.pool.Exec(ctx, "UPDATE collection_tasks SET heartbeat_at = now() - interval '2 hours' WHERE id = $1", first.ID)
	require.NoError(t, err)
	again, err := storer.ClaimCollectionTask(ctx, time.Hour)
	require.NoError(t, err)
	require.NotNil(t, again)
	assert.Equal(t, first.ID, again.ID)
	assert.Equal(t, 2, again.Attempts)
	running, err := storer.HeartbeatCollectionTask(ctx, again.ID)
	require.NoError(t, err)
	assert.True(t, running)

	// Released with a backoff: not claimed before it.
	require.NoError(t, storer.ReleaseCollectionTask(ctx, second.ID, "search quota", time.Now().Add(time.Hour), 3, 1, true))
	none, err = storer.ClaimCollectionTask(ctx, time.Hour)
	require.NoError(t, err)
	assert.Nil(t, none, "the released task waits for its not_before")
	require.NoError(t, storer.ReleaseCollectionTask(ctx, again.ID, "", time.Now(), 0, 0, false))
	third, err := storer.ClaimCollectionTask(ctx, time.Hour)
	require.NoError(t, err)
	require.NotNil(t, third)
	assert.Equal(t, first.ID, third.ID, "released for now: claimed again")
	assert.Equal(t, 2, third.Attempts, "a release that is not an attempt undoes the count of its claim")

	require.NoError(t, storer.FinishCollectionTask(ctx, third.ID, TaskDone, "", 10, 4))
	require.Error(t, storer.FinishCollectionTask(ctx, third.ID, TaskPending, "", 0, 0), "pending is not a final status")
	require.ErrorIs(t, storer.FinishCollectionTask(ctx, third.ID, TaskFailed, "late", 0, 0), errTaskNotRunning, "a finished task is not written again")
	n, err := storer.CancelCollectionTasks(ctx, []int64{second.ID, third.ID})
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the open task is cancelled")
	running, err = storer.HeartbeatCollectionTask(ctx, second.ID)
	require.NoError(t, err)
	assert.False(t, running, "the heartbeat of a cancelled task reports it")
	require.ErrorIs(t, storer.ReleaseCollectionTask(ctx, second.ID, "", time.Now(), 0, 0, true), errTaskNotRunning)

	tasks, err := storer.ListCollectionTasks(ctx, "", 10)
	require.NoError(t, err)
	byID := map[int64]CollectionTask{}
	for _, task := range tasks {
		byID[task.ID] = task
	}
	assert.Equal(t, TaskDone, byID[third.ID].Status)
	assert.Equal(t, 10, byID[third.ID].Items)
	assert.Equal(t, TaskCancelled, byID[second.ID].Status)
	assert.Equal(t, "search quota", byID[second.ID].LastError)
	assert.Equal(t, 3, byID[second.ID].Items, "the counts of the released attempt are kept")
	counts, err := storer.CountCollectionTasks(ctx)
	require.NoError(t, err)
	assert.Equal(t, map[string]int{TaskDone: 1, TaskCancelled: 1}, counts)
}

// TestCollectionTasksClaimSpreadsPersons: within a priority, the claim takes
// a task of a person no task runs for before an older task of a running
// person; a higher priority still goes first.
func TestCollectionTasksClaimSpreadsPersons(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE collection_tasks")
	require.NoError(t, err)
	a, err := storer.FindOrInsertPersonWithType(ctx, "spread person a", "human")
	require.NoError(t, err)
	b, err := storer.FindOrInsertPersonWithType(ctx, "spread person b", "human")
	require.NoError(t, err)
	day := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, nt := range []NewCollectionTask{
		{PersonID: a, Plugin: "news", Since: day, Until: day.AddDate(0, 0, 30)},
		{PersonID: a, Plugin: "twitter", Since: day, Until: day.AddDate(0, 0, 30)},
		{PersonID: b, Plugin: "news", Since: day, Until: day.AddDate(0, 0, 30)},
	} {
		_, err := storer.AddCollectionTasks(ctx, []NewCollectionTask{nt})
		require.NoError(t, err)
	}
	first, err := storer.ClaimCollectionTask(ctx, time.Hour)
	require.NoError(t, err)
	require.Equal(t, a, first.PersonID, "the oldest first")
	second, err := storer.ClaimCollectionTask(ctx, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, b, second.PersonID, "a person no task runs for before the older task of a")
	third, err := storer.ClaimCollectionTask(ctx, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, "twitter", third.Plugin, "then the task of a running person")
}
