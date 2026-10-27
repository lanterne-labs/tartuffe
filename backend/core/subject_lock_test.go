package core

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSubjectLocks: the holders of one subject, under any spelling, run one
// at a time; other subjects do not wait for them; no lock is left behind.
func TestSubjectLocks(t *testing.T) {
	var inFlight, peak atomic.Int32
	var wg sync.WaitGroup
	for _, name := range []string{"Édouard Philippe", "edouard philippe", "Edouard  PHILIPPE", "édouard-philippe"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			unlock := subjectLocks.Lock(subjectKey(name))
			defer unlock()
			n := inFlight.Add(1)
			for p := peak.Load(); n > p && !peak.CompareAndSwap(p, n); p = peak.Load() {
			}
			time.Sleep(10 * time.Millisecond)
			inFlight.Add(-1)
		}()
	}
	// Another subject is free while they wait.
	other := make(chan struct{})
	go func() {
		unlock := subjectLocks.Lock(subjectKey("Marine Le Pen"))
		unlock()
		close(other)
	}()
	select {
	case <-other:
	case <-time.After(time.Second):
		t.Fatal("another subject waited for the first")
	}
	wg.Wait()
	if peak.Load() != 1 {
		t.Errorf("%d holders of one subject at once, want 1", peak.Load())
	}
	subjectLocks.mu.Lock()
	left := len(subjectLocks.locks)
	subjectLocks.mu.Unlock()
	if left != 0 {
		t.Errorf("%d locks left behind", left)
	}
}
