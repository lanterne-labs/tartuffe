package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// clearPipelineJobs wipes the queue so a test starts from a known state. Skips
// the test if the table is not there (migrations not applied).
func clearPipelineJobs(ctx context.Context, t *testing.T, storer *PostgresStorer) {
	t.Helper()
	if _, err := storer.pool.Exec(ctx, "DELETE FROM pipeline_jobs"); err != nil {
		t.Skipf("pipeline_jobs table not present (run migrations): %v", err)
	}
}

// TestEnqueuePipelineJobDedupesActiveName verifies the queue's idempotency
// guarantee: while a job with a given name is pending or running, re-enqueueing
// that name is a no-op that returns the existing job. This is what stops a cron
// tick from stacking a second nightly run on top of one still in flight.
func TestEnqueuePipelineJobDedupesActiveName(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	clearPipelineJobs(ctx, t, storer)

	first, created, err := storer.EnqueuePipelineJob(ctx, "daily", []byte(`{}`), 100)
	require.NoError(t, err)
	assert.True(t, created)
	assert.Equal(t, PipelineJobQueued, first.Status)

	second, created, err := storer.EnqueuePipelineJob(ctx, "daily", []byte(`{}`), 100)
	require.NoError(t, err)
	assert.False(t, created, "second enqueue of an active name must not insert")
	assert.Equal(t, first.ID, second.ID)

	// Still deduped once it is running, not just while queued.
	claimed, err := storer.ClaimNextPipelineJob(ctx, "worker-a")
	require.NoError(t, err)
	require.NotNil(t, claimed)
	third, created, err := storer.EnqueuePipelineJob(ctx, "daily", []byte(`{}`), 100)
	require.NoError(t, err)
	assert.False(t, created)
	assert.Equal(t, PipelineJobRunning, third.Status)

	// Once terminal, the name frees up again.
	require.NoError(t, storer.FinishPipelineJob(ctx, first.ID, PipelineJobSuccess, nil, ""))
	fourth, created, err := storer.EnqueuePipelineJob(ctx, "daily", []byte(`{}`), 100)
	require.NoError(t, err)
	assert.True(t, created, "a finished name must be enqueueable again")
	assert.NotEqual(t, first.ID, fourth.ID)
}

// TestClaimNextPipelineJobOrderAndSingleFlight verifies claim order (priority,
// then FIFO) and that a claimed job is invisible to the next claim — the queue's
// one-at-a-time property.
func TestClaimNextPipelineJobOrderAndSingleFlight(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	clearPipelineJobs(ctx, t, storer)

	low, _, err := storer.EnqueuePipelineJob(ctx, "low-prio", []byte(`{}`), 100)
	require.NoError(t, err)
	urgent, _, err := storer.EnqueuePipelineJob(ctx, "urgent", []byte(`{"targets":["x"]}`), 10)
	require.NoError(t, err)

	first, err := storer.ClaimNextPipelineJob(ctx, "worker-a")
	require.NoError(t, err)
	require.NotNil(t, first)
	assert.Equal(t, urgent.ID, first.ID, "lower priority number runs first")
	assert.Equal(t, 1, first.Attempts)
	assert.JSONEq(t, `{"targets":["x"]}`, string(first.Config), "config round-trips verbatim")

	second, err := storer.ClaimNextPipelineJob(ctx, "worker-b")
	require.NoError(t, err)
	require.NotNil(t, second)
	assert.Equal(t, low.ID, second.ID, "a running job is never handed out twice")

	empty, err := storer.ClaimNextPipelineJob(ctx, "worker-c")
	require.NoError(t, err)
	assert.Nil(t, empty, "an empty queue claims nothing")
}

// TestReapStalePipelineJobs verifies crash recovery: a running job whose worker
// stopped heartbeating is requeued while it has attempts left, and failed once
// the budget is exhausted so a crash-looping job cannot spin forever.
func TestReapStalePipelineJobs(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	clearPipelineJobs(ctx, t, storer)

	job, _, err := storer.EnqueuePipelineJob(ctx, "reapable", []byte(`{}`), 100)
	require.NoError(t, err)
	claimed, err := storer.ClaimNextPipelineJob(ctx, "dead-worker")
	require.NoError(t, err)
	require.NotNil(t, claimed)

	// A live heartbeat protects the job.
	require.NoError(t, storer.HeartbeatPipelineJob(ctx, job.ID, "dead-worker"))
	requeued, failed, err := storer.ReapStalePipelineJobs(ctx, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 0, requeued)
	assert.Equal(t, 0, failed)

	// Simulate the worker dying: age the heartbeat past the threshold.
	ageHeartbeat := func(id int64, d time.Duration) {
		_, err := storer.pool.Exec(ctx,
			`UPDATE pipeline_jobs SET heartbeat_at = now() - $2::interval WHERE id = $1`, id, d.String())
		require.NoError(t, err)
	}
	ageHeartbeat(job.ID, 2*time.Hour)

	requeued, failed, err = storer.ReapStalePipelineJobs(ctx, time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, requeued)
	assert.Equal(t, 0, failed)

	back, err := storer.GetPipelineJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, PipelineJobQueued, back.Status)
	assert.Nil(t, back.WorkerID)
	assert.Equal(t, 1, back.Attempts, "the failed attempt still counts")

	// Burn the remaining attempts; the last reap must fail the job, not requeue it.
	for i := 0; i < back.MaxAttempts; i++ {
		c, err := storer.ClaimNextPipelineJob(ctx, "dead-worker")
		require.NoError(t, err)
		require.NotNil(t, c)
		ageHeartbeat(job.ID, 2*time.Hour)
		requeued, failed, err = storer.ReapStalePipelineJobs(ctx, time.Hour)
		require.NoError(t, err)
		if failed == 1 {
			break
		}
	}
	assert.Equal(t, 1, failed, "attempts exhausted must terminate the job")

	dead, err := storer.GetPipelineJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, PipelineJobError, dead.Status)
	require.NotNil(t, dead.Error)
	assert.Contains(t, *dead.Error, "no heartbeat")
	assert.NotNil(t, dead.FinishedAt)
}

// TestReleasePipelineJob verifies the graceful-shutdown path: an interrupted job
// returns to the queue without consuming the attempt it was using.
func TestReleasePipelineJob(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	clearPipelineJobs(ctx, t, storer)

	job, _, err := storer.EnqueuePipelineJob(ctx, "interrupted", []byte(`{}`), 100)
	require.NoError(t, err)
	claimed, err := storer.ClaimNextPipelineJob(ctx, "worker-a")
	require.NoError(t, err)
	require.Equal(t, 1, claimed.Attempts)

	require.NoError(t, storer.ReleasePipelineJob(ctx, job.ID, "worker-a"))

	back, err := storer.GetPipelineJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, PipelineJobQueued, back.Status)
	assert.Equal(t, 0, back.Attempts, "a redeploy must not spend the job's attempt budget")
	assert.Nil(t, back.StartedAt)

	// Releasing a job this worker no longer owns is an error, never a silent no-op.
	require.Error(t, storer.ReleasePipelineJob(ctx, job.ID, "worker-a"))
}

// TestCancelPipelineJob verifies only pending jobs are cancelable.
func TestCancelPipelineJob(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	clearPipelineJobs(ctx, t, storer)

	job, _, err := storer.EnqueuePipelineJob(ctx, "cancelable", []byte(`{}`), 100)
	require.NoError(t, err)
	require.NoError(t, storer.CancelPipelineJob(ctx, job.ID))

	got, err := storer.GetPipelineJob(ctx, job.ID)
	require.NoError(t, err)
	assert.Equal(t, PipelineJobCanceled, got.Status)

	running, _, err := storer.EnqueuePipelineJob(ctx, "already-running", []byte(`{}`), 100)
	require.NoError(t, err)
	_, err = storer.ClaimNextPipelineJob(ctx, "worker-a")
	require.NoError(t, err)
	assert.Error(t, storer.CancelPipelineJob(ctx, running.ID), "a running job cannot be canceled from the queue")
}

// TestListPipelineJobsPendingFirst verifies the listing puts pending work at the
// top in claim order, with history after it.
func TestListPipelineJobsPendingFirst(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	clearPipelineJobs(ctx, t, storer)

	done, _, err := storer.EnqueuePipelineJob(ctx, "finished", []byte(`{}`), 100)
	require.NoError(t, err)
	_, err = storer.ClaimNextPipelineJob(ctx, "worker-a")
	require.NoError(t, err)
	require.NoError(t, storer.FinishPipelineJob(ctx, done.ID, PipelineJobSuccess, nil, ""))

	_, _, err = storer.EnqueuePipelineJob(ctx, "waiting-normal", []byte(`{}`), 100)
	require.NoError(t, err)
	urgent, _, err := storer.EnqueuePipelineJob(ctx, "waiting-urgent", []byte(`{}`), 1)
	require.NoError(t, err)

	jobs, err := storer.ListPipelineJobs(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, jobs, 3)
	assert.Equal(t, urgent.ID, jobs[0].ID, "highest-priority pending job leads")
	assert.Equal(t, "waiting-normal", jobs[1].Name)
	assert.Equal(t, done.ID, jobs[2].ID, "terminal jobs come after pending ones")

	only, err := storer.ListPipelineJobs(ctx, PipelineJobSuccess, 10)
	require.NoError(t, err)
	require.Len(t, only, 1)
	assert.Equal(t, done.ID, only[0].ID)
}
