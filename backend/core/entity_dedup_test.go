package core

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"digital-mirror/backend/llm"
)

// normalizeClusters sorts members within each cluster and clusters by their
// first member so the result is order-independent for comparison.
func normalizeClusters(clusters [][]int64) [][]int64 {
	out := make([][]int64, len(clusters))
	for i, c := range clusters {
		cp := append([]int64(nil), c...)
		sort.Slice(cp, func(a, b int) bool { return cp[a] < cp[b] })
		out[i] = cp
	}
	sort.Slice(out, func(a, b int) bool { return out[a][0] < out[b][0] })
	return out
}

func equalClusters(a, b [][]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

func TestClusterPairs(t *testing.T) {
	cases := []struct {
		name  string
		pairs [][2]int64
		want  [][]int64
	}{
		{
			name:  "transitive chain merges into one cluster",
			pairs: [][2]int64{{1, 2}, {2, 3}},
			want:  [][]int64{{1, 2, 3}},
		},
		{
			name:  "two disjoint clusters",
			pairs: [][2]int64{{1, 2}, {3, 4}},
			want:  [][]int64{{1, 2}, {3, 4}},
		},
		{
			name:  "duplicate pairs are idempotent",
			pairs: [][2]int64{{1, 2}, {2, 1}, {1, 2}},
			want:  [][]int64{{1, 2}},
		},
		{
			name:  "empty input yields no clusters",
			pairs: nil,
			want:  [][]int64{},
		},
		{
			name:  "star topology collapses to one cluster",
			pairs: [][2]int64{{10, 11}, {10, 12}, {10, 13}},
			want:  [][]int64{{10, 11, 12, 13}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := normalizeClusters(ClusterPairs(tc.pairs))
			want := normalizeClusters(tc.want)
			if !equalClusters(got, want) {
				t.Fatalf("ClusterPairs(%v) = %v, want %v", tc.pairs, got, want)
			}
		})
	}
}

// entityDedupStub answers each call with the next reply of replies (the last
// one repeats) and counts the calls.
func entityDedupStub(t *testing.T, replies ...string) (*EntityDedupAdjudicator, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		reply := replies[min(n, len(replies))-1]
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": reply}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	cfg := llm.Config{BaseURL: srv.URL + "/v1", APIKey: "test", ChatModel: "stub"}
	return &EntityDedupAdjudicator{client: cfg.NewClient("entity-dedup-test")}, &calls
}

// A prose reply gets one corrective retry; the JSON of the second reply is
// the verdict.
func TestAdjudicatePair_CorrectiveRetry(t *testing.T) {
	a, calls := entityDedupStub(t, "A quick check shows the same party.", `{"reasoning": "acronym", "same": true}`)
	v, err := a.AdjudicatePair(context.Background(), EntityRef{ID: 1, Name: "rassemblement national"}, EntityRef{ID: 2, Name: "rn"})
	if err != nil {
		t.Fatalf("AdjudicatePair: %v", err)
	}
	if !v.Same || v.Reasoning != "acronym" {
		t.Fatalf("verdict = %+v, want same=true reasoning=acronym", v)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
}

// A second prose reply is an error, never a default verdict.
func TestAdjudicatePair_TwiceInvalid(t *testing.T) {
	a, calls := entityDedupStub(t, "A quick check shows the same party.")
	_, err := a.AdjudicatePair(context.Background(), EntityRef{ID: 1, Name: "rassemblement national"}, EntityRef{ID: 2, Name: "rn"})
	if err == nil || !strings.Contains(err.Error(), "invalid JSON twice") {
		t.Fatalf("err = %v, want an error on the second invalid reply", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
}
