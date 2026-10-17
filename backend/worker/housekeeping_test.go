package worker

import (
	"context"
	"testing"
	"time"

	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHousekeepingDue: the feed is due once a day after the feed hour; the
// maintenance pass is due after facts were added, on the first pass, and at
// least every MaintenanceEvery; the next pass judges what changed after the
// start of the last one.
func TestHousekeepingDue(t *testing.T) {
	storer := testStorer(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "DELETE FROM job_metrics WHERE job_type IN ($1, $2)", feedJob, maintenanceJob)
	require.NoError(t, err)
	clock := time.Date(2026, 10, 18, 2, 30, 0, 0, time.UTC)
	h := &Housekeeping{Storer: storer, MaintenanceEvery: 6 * time.Hour, FeedHourUTC: 3, Now: func() time.Time { return clock }}
	// A job metric recorded at a time of the mocked clock.
	record := func(job, metric string, value float64, at time.Time) {
		_, err := storer.Pool().Exec(ctx, "INSERT INTO job_metrics (job_type, target, metric, value, created_at) VALUES ($1, '', $2, $3, $4)", job, metric, value, at)
		require.NoError(t, err)
	}

	due, err := h.feedDue(ctx)
	require.NoError(t, err)
	assert.True(t, due, "no feed ran yet")
	record(feedJob, "facts", 1, time.Date(2026, 10, 17, 10, 0, 0, 0, time.UTC))
	due, err = h.feedDue(ctx)
	require.NoError(t, err)
	assert.False(t, due, "the feed of the day (yesterday 03:00 to today 03:00) ran")
	clock = clock.Add(time.Hour) // 03:30: a new feed day
	due, err = h.feedDue(ctx)
	require.NoError(t, err)
	assert.True(t, due, "the feed hour passed since the last feed")

	due, since, err := h.maintenanceDue(ctx)
	require.NoError(t, err)
	assert.True(t, due, "the first pass is due")
	assert.True(t, since.IsZero(), "the first pass judges everything")
	// A pass of 10 min ends now.
	record(maintenanceJob, "duration_seconds", 600, clock)
	due, since, err = h.maintenanceDue(ctx)
	require.NoError(t, err)
	assert.False(t, due, "nothing changed since the pass")
	assert.Equal(t, clock.Add(-10*time.Minute), since.UTC(), "the next pass judges what changed after the start of the last one")
	h.FactsAdded(0)
	due, _, err = h.maintenanceDue(ctx)
	require.NoError(t, err)
	assert.False(t, due, "a task without facts changes nothing")
	h.FactsAdded(3)
	due, _, err = h.maintenanceDue(ctx)
	require.NoError(t, err)
	assert.True(t, due, "facts were added")
	h.dirty = false
	clock = clock.Add(7 * time.Hour)
	due, _, err = h.maintenanceDue(ctx)
	require.NoError(t, err)
	assert.True(t, due, "MaintenanceEvery passed")
}

// fakeIdle records the idle calls and the facts reported.
type fakeIdle struct {
	calls int
	facts int
	done  chan struct{}
}

func (f *fakeIdle) Idle(context.Context) error {
	f.calls++
	if f.calls == 1 {
		close(f.done)
	}
	return nil
}

func (f *fakeIdle) FactsAdded(n int) { f.facts += n }

// TestWorkerIdleRunsTheHousekeeping: the worker tells the housekeeping about
// the facts of a done task and runs it when no task is pending.
func TestWorkerIdleRunsTheHousekeeping(t *testing.T) {
	storer := testStorer(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE collection_tasks")
	require.NoError(t, err)
	person, err := storer.FindOrInsertPersonWithType(ctx, "worker person", "human")
	require.NoError(t, err)
	day := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	_, err = storer.AddCollectionTasks(ctx, []storage.NewCollectionTask{{PersonID: person, Plugin: "ok", Since: day, Until: day.AddDate(0, 0, 3)}})
	require.NoError(t, err)
	runner := &fakeRunner{want: 1, done: make(chan struct{}), script: map[string]func(storage.CollectionTask) (Outcome, error){
		"ok": func(storage.CollectionTask) (Outcome, error) { return Outcome{Items: 2, Facts: 7, Covered: true}, nil },
	}}
	idle := &fakeIdle{done: make(chan struct{})}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	errc := make(chan error, 1)
	opts := Options{Poll: 20 * time.Millisecond, Lease: time.Minute, MaxAttempts: 1, Backoff: time.Hour, MaxLLMWait: time.Second}
	go func() { errc <- Run(runCtx, storer, runner, idle, opts) }()
	select {
	case <-idle.done:
	case <-time.After(5 * time.Second):
		t.Fatal("the housekeeping did not run")
	}
	stop()
	require.ErrorIs(t, <-errc, context.Canceled)
	assert.Len(t, runner.ran, 1, "the task ran before the housekeeping")
	assert.Equal(t, 7, idle.facts, "the facts of the done task were reported")
}
