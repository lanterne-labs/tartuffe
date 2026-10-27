package worker

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// farResetIdle fails its housekeeping (Beside, or Idle) as an unavailable
// LLM whose reset is at `at`, and records when it was called.
type farResetIdle struct {
	mu     sync.Mutex
	calls  []time.Time
	at     time.Time
	beside bool // the failing work is Beside (the feed), else Idle (the pass)
}

func (f *farResetIdle) fail() error {
	f.mu.Lock()
	f.calls = append(f.calls, time.Now())
	f.mu.Unlock()
	return &llm.UnavailableError{Endpoint: llm.EndpointClaudeTeam, RetryAt: f.at,
		Err: fmt.Errorf("%w: every claude seat (4) is at its usage limit", llm.ErrUnavailable)}
}

func (f *farResetIdle) called() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.calls...)
}

func (f *farResetIdle) Idle(context.Context) error {
	if f.beside {
		return nil
	}
	return f.fail()
}

func (f *farResetIdle) Beside(context.Context) error {
	if !f.beside {
		return nil
	}
	return f.fail()
}

func (f *farResetIdle) Urgent(context.Context) (bool, error)     { return false, nil }
func (f *farResetIdle) FactsAdded(int)                           {}
func (f *farResetIdle) MinPriority(context.Context) (int, error) { return 0, nil }

// TestWorkerHousekeepingWaitsForAFarReset: a housekeeping cut by an LLM whose
// reset is after the max wait is tried again at the reset, not at every poll
// (2026-10-27: the feed re-ran every minute for 80 min while the seats were
// at their limit and the API at its day cap).
func TestWorkerHousekeepingWaitsForAFarReset(t *testing.T) {
	for _, work := range []string{"beside", "idle"} {
		t.Run(work, func(t *testing.T) {
			idle := &farResetIdle{at: time.Now().Add(300 * time.Millisecond), beside: work == "beside"}
			tasks := &memTasks{finished: map[int64]string{}}
			runner := &fakeRunner{script: map[string]func(storage.CollectionTask) (Outcome, error){}}
			opts := Options{Poll: 20 * time.Millisecond, Lease: time.Minute, MaxAttempts: 1, Backoff: time.Hour, MaxLLMWait: 50 * time.Millisecond}
			runCtx, stop := context.WithCancel(context.Background())
			defer stop()
			errc := make(chan error, 1)
			go func() { errc <- Run(runCtx, tasks, runner, idle, opts) }()

			time.Sleep(200 * time.Millisecond)
			assert.Len(t, idle.called(), 1, "not tried again before the reset")
			require.Eventually(t, func() bool { return len(idle.called()) >= 2 }, 2*time.Second, 5*time.Millisecond, "tried again at the reset")
			stop()
			require.ErrorIs(t, <-errc, context.Canceled)
			calls := idle.called()
			assert.False(t, calls[1].Before(idle.at), "the second try waited for the reset")
		})
	}
}
