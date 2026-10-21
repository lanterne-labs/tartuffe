package llm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// forEachPeak runs ForEach over n items that each hold a slot for a moment,
// and returns the most items in flight at once and the order they started in.
func forEachPeak(t *testing.T, n int, fail int) (peak int32, started []int, err error) {
	t.Helper()
	items := make([]int, n)
	for i := range items {
		items[i] = i
	}
	var inFlight atomic.Int32
	var mu sync.Mutex
	err = ForEach(context.Background(), items, func(ctx context.Context, i int) error {
		mu.Lock()
		started = append(started, i)
		mu.Unlock()
		cur := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			old := atomic.LoadInt32(&peak)
			if cur <= old || atomic.CompareAndSwapInt32(&peak, old, cur) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		if i == fail {
			return errors.New("boom")
		}
		return nil
	})
	return peak, started, err
}

func TestForEach(t *testing.T) {
	// The local endpoint serves one call: one item at a time, in order.
	peak, started, err := forEachPeak(t, 6, -1)
	if err != nil || peak != 1 {
		t.Fatalf("local: peak %d, err %v; want 1, nil", peak, err)
	}
	for i, got := range started {
		if got != i {
			t.Fatalf("local: started %v, want in order", started)
		}
	}
	_, started, err = forEachPeak(t, 6, 2)
	if err == nil || len(started) != 3 {
		t.Fatalf("local: an error at item 2 started %v (err %v); want 0..2 and the error", started, err)
	}

	restore, err := SetRouting(ClaudeRouting(EndpointClaudeTeam, &ClaudeTeamEndpoint{MaxProcs: 3}, "claude-sonnet-5-5"))
	if err != nil {
		t.Fatal(err)
	}
	defer restore()
	peak, started, err = forEachPeak(t, 9, -1)
	if err != nil || peak != 3 || len(started) != 9 {
		t.Fatalf("team: peak %d, started %d, err %v; want 3, 9, nil", peak, len(started), err)
	}
	_, started, err = forEachPeak(t, 30, 0)
	if err == nil || len(started) >= 30 {
		t.Fatalf("team: an error at the first item started %d items (err %v); want fewer than all and the error", len(started), err)
	}
}
