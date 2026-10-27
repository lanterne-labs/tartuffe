package worker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeRunner answers each task from its script, by plugin, and records the
// tasks it ran.
type fakeRunner struct {
	mu     sync.Mutex
	script map[string]func(task storage.CollectionTask) (Outcome, error)
	ran    []int64
	done   chan struct{} // closed when ran reaches want
	want   int
	// ctxDone is the Done channel of the context of the last attempt.
	ctxDone <-chan struct{}
}

func (f *fakeRunner) Run(ctx context.Context, task storage.CollectionTask) (Outcome, error) {
	f.mu.Lock()
	f.ctxDone = ctx.Done()
	f.ran = append(f.ran, task.ID)
	if len(f.ran) == f.want {
		close(f.done)
	}
	f.mu.Unlock()
	return f.script[task.Plugin](task)
}

func testStorer(t *testing.T) *storage.PostgresStorer {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set")
	}
	storer, err := storage.NewPostgresStorerFromEnv(context.Background())
	require.NoError(t, err)
	t.Cleanup(storer.Close)
	return storer
}

// TestWorkerRunsTasksAndRecordsOutcomes: a covered task is done; an attempt
// that fails is retried after the backoff and fails for good at MaxAttempts;
// an LLM outage puts the task back to pending and the worker waits for the
// reset; a stop of the worker puts its task back to pending at once.
func TestWorkerRunsTasksAndRecordsOutcomes(t *testing.T) {
	storer := testStorer(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE collection_tasks")
	require.NoError(t, err)
	person, err := storer.FindOrInsertPersonWithType(ctx, "worker person", "human")
	require.NoError(t, err)
	day := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	_, err = storer.AddCollectionTasks(ctx, []storage.NewCollectionTask{
		{PersonID: person, Plugin: "ok", Since: day, Until: day.AddDate(0, 0, 3), Priority: 3},
		{PersonID: person, Plugin: "quota", Since: day, Until: day.AddDate(0, 0, 3), Priority: 2},
		{PersonID: person, Plugin: "outage", Since: day, Until: day.AddDate(0, 0, 3), Priority: 1},
		{PersonID: person, Plugin: "searchquota", Since: day, Until: day.AddDate(0, 0, 3), Priority: 0},
	})
	require.NoError(t, err)

	outages := 0
	runner := &fakeRunner{want: 5, done: make(chan struct{}), script: map[string]func(storage.CollectionTask) (Outcome, error){
		"searchquota": func(storage.CollectionTask) (Outcome, error) {
			return Outcome{}, fmt.Errorf("window 1/30: %w", search.ErrQuota)
		},
		"ok":    func(storage.CollectionTask) (Outcome, error) { return Outcome{Items: 2, Facts: 1, Covered: true}, nil },
		"quota": func(storage.CollectionTask) (Outcome, error) { return Outcome{Details: []string{"search quota"}}, nil },
		"outage": func(storage.CollectionTask) (Outcome, error) {
			outages++
			if outages == 1 {
				return Outcome{}, &llm.UnavailableError{Endpoint: llm.EndpointClaudeTeam, RetryAt: time.Now().Add(50 * time.Millisecond), Err: llm.ErrUnavailable}
			}
			return Outcome{Covered: true}, nil
		},
	}}
	opts := Options{Poll: 20 * time.Millisecond, Lease: time.Minute, MaxAttempts: 1, Backoff: time.Hour, MaxLLMWait: time.Second}
	runCtx, stop := context.WithCancel(ctx)
	errc := make(chan error, 1)
	go func() { errc <- Run(runCtx, storer, runner, nil, opts) }()
	select {
	case <-runner.done:
	case <-time.After(10 * time.Second):
		t.Fatal("the worker did not run the 5 attempts in time")
	}
	// The 5th attempt started; its release is recorded before the stop, or
	// the stop releases it as stopped.
	require.Eventually(t, func() bool {
		var released bool
		err := storer.Pool().QueryRow(ctx, "SELECT status = 'pending' FROM collection_tasks WHERE plugin = 'searchquota'").Scan(&released)
		return err == nil && released
	}, 5*time.Second, 10*time.Millisecond, "the search quota task is released")
	stop()
	require.ErrorIs(t, <-errc, context.Canceled)

	tasks, err := storer.ListCollectionTasks(ctx, "", 10)
	require.NoError(t, err)
	byPlugin := map[string]storage.CollectionTask{}
	for _, task := range tasks {
		byPlugin[task.Plugin] = task
	}
	assert.Equal(t, []int64{byPlugin["ok"].ID, byPlugin["quota"].ID, byPlugin["outage"].ID, byPlugin["outage"].ID, byPlugin["searchquota"].ID}, runner.ran,
		"by priority; the outage task runs again after the reset")
	assert.Equal(t, storage.TaskDone, byPlugin["ok"].Status)
	assert.Equal(t, 1, byPlugin["ok"].Facts)
	assert.Equal(t, storage.TaskFailed, byPlugin["quota"].Status, "not covered after MaxAttempts: failed")
	assert.Contains(t, byPlugin["quota"].LastError, "search quota")
	assert.Equal(t, storage.TaskDone, byPlugin["outage"].Status)
	assert.Equal(t, 1, byPlugin["outage"].Attempts, "the outage is not an attempt of the task")
	sq := byPlugin["searchquota"]
	assert.Equal(t, storage.TaskPending, sq.Status, "a search quota is a wait, not a failure")
	assert.Equal(t, 0, sq.Attempts, "the search quota is not an attempt of the task")
	assert.True(t, sq.NotBefore.After(time.Now().Add(time.Hour)), "the task waits for the quota")
}

// TestWorkerCancelStopsTheAttempt: a task cancelled while it runs is stopped
// at the next heartbeat, and its row stays cancelled.
func TestWorkerCancelStopsTheAttempt(t *testing.T) {
	storer := testStorer(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE collection_tasks")
	require.NoError(t, err)
	person, err := storer.FindOrInsertPersonWithType(ctx, "worker person", "human")
	require.NoError(t, err)
	day := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	_, err = storer.AddCollectionTasks(ctx, []storage.NewCollectionTask{{PersonID: person, Plugin: "long", Since: day, Until: day.AddDate(0, 0, 3)}})
	require.NoError(t, err)

	started := make(chan int64, 1)
	runner := &fakeRunner{want: 1, done: make(chan struct{}), script: map[string]func(storage.CollectionTask) (Outcome, error){
		"long": func(storage.CollectionTask) (Outcome, error) { return Outcome{Covered: true}, nil },
	}}
	runner.script["long"] = func(task storage.CollectionTask) (Outcome, error) {
		started <- task.ID
		<-runner.ctxDone
		return Outcome{Items: 1}, fmt.Errorf("stopped: %w", context.Canceled)
	}
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	errc := make(chan error, 1)
	opts := Options{Poll: 20 * time.Millisecond, Lease: 150 * time.Millisecond, MaxAttempts: 3, Backoff: time.Hour, MaxLLMWait: time.Second}
	go func() { errc <- Run(runCtx, storer, runner, nil, opts) }()
	id := <-started
	n, err := storer.CancelCollectionTasks(ctx, []int64{id})
	require.NoError(t, err)
	require.Equal(t, 1, n)
	select {
	case <-runner.ctxDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the attempt was not stopped after the cancel")
	}
	time.Sleep(100 * time.Millisecond) // the bookkeeping of the dropped attempt
	stop()
	require.ErrorIs(t, <-errc, context.Canceled)
	tasks, err := storer.ListCollectionTasks(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, storage.TaskCancelled, tasks[0].Status, "the cancelled row is not written by the dropped attempt")
	assert.Equal(t, 0, tasks[0].Items)
}

// TestWorkerStopReleasesTheTask: when the worker stops in the middle of a
// task, the task goes back to pending with no backoff.
func TestWorkerStopReleasesTheTask(t *testing.T) {
	storer := testStorer(t)
	ctx := context.Background()
	_, err := storer.Pool().Exec(ctx, "TRUNCATE collection_tasks")
	require.NoError(t, err)
	person, err := storer.FindOrInsertPersonWithType(ctx, "worker person", "human")
	require.NoError(t, err)
	day := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	_, err = storer.AddCollectionTasks(ctx, []storage.NewCollectionTask{{PersonID: person, Plugin: "slow", Since: day, Until: day.AddDate(0, 0, 3)}})
	require.NoError(t, err)

	runCtx, stop := context.WithCancel(ctx)
	started := make(chan struct{})
	runner := &fakeRunner{want: 1, done: make(chan struct{}), script: map[string]func(storage.CollectionTask) (Outcome, error){
		"slow": func(storage.CollectionTask) (Outcome, error) {
			close(started)
			<-runCtx.Done()
			return Outcome{Items: 1}, fmt.Errorf("window stopped: %w", runCtx.Err())
		},
	}}
	errc := make(chan error, 1)
	go func() { errc <- Run(runCtx, storer, runner, nil, DefaultOptions) }()
	<-started
	stop()
	require.ErrorIs(t, <-errc, context.Canceled)

	tasks, err := storer.ListCollectionTasks(ctx, "", 10)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, storage.TaskPending, tasks[0].Status)
	assert.Equal(t, "worker stopped", tasks[0].LastError)
	assert.Equal(t, 1, tasks[0].Items, "the items of the cut attempt are kept")
	assert.False(t, tasks[0].NotBefore.After(time.Now()), "no backoff: claimed again at once")
}
