package contradiction

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestJudgeConcurrently returns the verdicts in pair order. On the local
// endpoint, which serves one call, the calls go one at a time (llm.ForEach;
// its own test covers an endpoint that serves several).
func TestJudgeConcurrently(t *testing.T) {
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		body, _ := io.ReadAll(r.Body)
		verdict := "NO: different subjects"
		if strings.Contains(string(body), "flip") {
			verdict = "YES: reversal"
		}
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": "PAIR 1: " + verdict}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_API", srv.URL+"/v1")
	detector, err := NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	pairs := []pairInfo{
		{factAText: "a0", factBText: "b0"},
		{factAText: "flip a1", factBText: "b1"},
		{factAText: "a2", factBText: "b2"},
		{factAText: "flip a3", factBText: "b3"},
	}
	results := judgeConcurrently(context.Background(), detector, pairs, 1)
	if len(results) != len(pairs) {
		t.Fatalf("got %d results, want %d", len(results), len(pairs))
	}
	for i, r := range results {
		if r.Error != nil {
			t.Fatalf("pair %d: %v", i, r.Error)
		}
		if want := i%2 == 1; r.IsContradictory != want {
			t.Errorf("pair %d: contradictory=%v, want %v", i, r.IsContradictory, want)
		}
	}
	if peak.Load() != 1 {
		t.Errorf("peak in-flight calls = %d, want 1 on the local endpoint", peak.Load())
	}
}
