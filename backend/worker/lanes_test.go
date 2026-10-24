package worker

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
)

// memTasks is an in-memory task queue: Claim hands out the pending tasks in
// order.
type memTasks struct {
	mu       sync.Mutex
	pending  []storage.CollectionTask
	finished map[int64]string
}

func (m *memTasks) ClaimCollectionTask(context.Context, time.Duration) (*storage.CollectionTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.pending) == 0 {
		return nil, nil
	}
	t := m.pending[0]
	m.pending = m.pending[1:]
	t.Attempts++
	return &t, nil
}

func (m *memTasks) HeartbeatCollectionTask(context.Context, int64) (bool, error) { return true, nil }

func (m *memTasks) FinishCollectionTask(_ context.Context, id int64, status, _ string, _, _ int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.finished[id] = status
	return nil
}

func (m *memTasks) ReleaseCollectionTask(context.Context, int64, string, time.Time, int, int, bool) error {
	return nil
}

// laneRunner holds each task a moment and records the most tasks at once.
type laneRunner struct {
	inFlight, peak atomic.Int32
}

func (r *laneRunner) Run(context.Context, storage.CollectionTask) (Outcome, error) {
	n := r.inFlight.Add(1)
	defer r.inFlight.Add(-1)
	for {
		p := r.peak.Load()
		if n <= p || r.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(30 * time.Millisecond)
	return Outcome{Covered: true, Facts: 1}, nil
}

// laneIdle records whether the housekeeping ever ran while a task ran.
type laneIdle struct {
	runner        *laneRunner
	calls         atomic.Int32
	overlap       atomic.Bool
	besideOverlap atomic.Bool // Beside ran while a task ran
	besideIn      atomic.Bool // Beside runs now
	besideFor     time.Duration
	idleBeside    atomic.Bool // Idle ran while Beside ran
	urgent        atomic.Bool // Urgent says yes until Idle ran
	urgentAsked   atomic.Bool
	pendingAtIdle atomic.Int32 // the tasks still pending when Idle first ran
	tasks         *memTasks
}

func (i *laneIdle) Idle(context.Context) error {
	if i.calls.Add(1) == 1 && i.tasks != nil {
		i.tasks.mu.Lock()
		i.pendingAtIdle.Store(int32(len(i.tasks.pending)))
		i.tasks.mu.Unlock()
	}
	if i.runner.inFlight.Load() > 0 {
		i.overlap.Store(true)
	}
	if i.besideIn.Load() {
		i.idleBeside.Store(true)
	}
	return nil
}

func (i *laneIdle) FactsAdded(int) {}

func (i *laneIdle) Beside(context.Context) error {
	i.besideIn.Store(true)
	defer i.besideIn.Store(false)
	if i.runner.inFlight.Load() > 0 {
		i.besideOverlap.Store(true)
	}
	time.Sleep(i.besideFor)
	return nil
}

func (i *laneIdle) Urgent(context.Context) (bool, error) {
	if i.urgent.Load() && i.calls.Load() == 0 {
		i.urgentAsked.Store(true)
		return true, nil
	}
	return false, nil
}

// runLanes runs the worker over n tasks until they are done and the
// housekeeping ran, and returns the most tasks that ran at once.
func runLanes(t *testing.T, n int, urgent ...bool) (int32, *laneIdle) {
	t.Helper()
	tasks := &memTasks{finished: map[int64]string{}}
	for id := int64(1); id <= int64(n); id++ {
		tasks.pending = append(tasks.pending, storage.CollectionTask{ID: id, Plugin: "news", PersonName: "p"})
	}
	runner := &laneRunner{}
	idle := &laneIdle{runner: runner, tasks: tasks, besideFor: 25 * time.Millisecond}
	idle.urgent.Store(len(urgent) > 0 && urgent[0])
	ctx, cancel := context.WithCancel(context.Background())
	opts := DefaultOptions
	opts.Poll = 10 * time.Millisecond
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, tasks, runner, idle, opts) }()
	deadline := time.Now().Add(5 * time.Second)
	for {
		tasks.mu.Lock()
		finished := len(tasks.finished)
		tasks.mu.Unlock()
		// The housekeeping runs once the queue is empty and the feed ended.
		if finished == n && idle.calls.Load() > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-errc
	tasks.mu.Lock()
	defer tasks.mu.Unlock()
	if len(tasks.finished) != n {
		t.Fatalf("%d of %d tasks finished", len(tasks.finished), n)
	}
	for id, status := range tasks.finished {
		if status != storage.TaskDone {
			t.Errorf("task %d: %s, want done", id, status)
		}
	}
	return runner.peak.Load(), idle
}

// TestWorkerLanes: the worker runs as many tasks at once as the endpoint
// serves (one on the local endpoint), and never does the housekeeping while
// a task runs.
func TestWorkerLanes(t *testing.T) {
	peak, idle := runLanes(t, 6)
	if peak != 1 {
		t.Errorf("local endpoint: %d tasks at once, want 1", peak)
	}
	if idle.overlap.Load() {
		t.Error("local endpoint: the housekeeping ran while a task ran")
	}

	restore, err := llm.SetRouting(llm.ClaudeRouting(llm.EndpointClaudeTeam, &llm.ClaudeTeamEndpoint{ProcsPerSeat: 3}, "claude-sonnet-5-5"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	peak, idle = runLanes(t, 9)
	if peak != 3 {
		t.Errorf("team endpoint: %d tasks at once, want 3", peak)
	}
	if idle.overlap.Load() {
		t.Error("team endpoint: the housekeeping ran while a task ran")
	}
	if idle.calls.Load() == 0 {
		t.Error("team endpoint: no housekeeping once the queue was empty")
	}
}

// TestWorkerUrgentHousekeeping: an urgent maintenance stops the claims, lets
// the running tasks end, runs alone while tasks are still pending, and the
// tasks go on after it; the work beside the tasks runs while they run, never
// with the maintenance.
func TestWorkerUrgentHousekeeping(t *testing.T) {
	restore, err := llm.SetRouting(llm.ClaudeRouting(llm.EndpointClaudeTeam, &llm.ClaudeTeamEndpoint{ProcsPerSeat: 3}, "claude-sonnet-5-5"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	_, idle := runLanes(t, 30, true)
	if !idle.urgentAsked.Load() || idle.calls.Load() == 0 {
		t.Fatal("the urgent maintenance did not run")
	}
	if idle.overlap.Load() {
		t.Error("the maintenance ran while a task ran")
	}
	if idle.pendingAtIdle.Load() == 0 {
		t.Error("the maintenance waited for the queue to be empty")
	}
	if !idle.besideOverlap.Load() {
		t.Error("the work beside the tasks never ran while a task ran")
	}
	if idle.idleBeside.Load() {
		t.Error("the maintenance ran while the work beside the tasks ran")
	}
}
