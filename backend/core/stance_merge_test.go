package core

import (
	"digital-mirror/backend/util"
	"reflect"
	"testing"
)

// TestGroupByConfirmedSimilarPairs: similar embeddings become candidate pairs,
// the confirm() gate decides, and confirmed pairs union-find into one group.
func TestGroupByConfirmedSimilarPairs(t *testing.T) {
	embs := [][]float32{
		util.L2Normalize([]float32{1, 0}),       // 0
		util.L2Normalize([]float32{0.98, 0.2}),  // 1 — close to 0
		util.L2Normalize([]float32{0.95, 0.31}), // 2 — close to 0 and 1
		util.L2Normalize([]float32{0, 1}),       // 3 — far from the rest
	}
	// 0,1,2 are the same subject; anything touching 3 is rejected (and 3 isn't
	// even a candidate by similarity).
	confirm := func(i, j int, _ bool) bool { return i != 3 && j != 3 }
	got := groupByConfirmedSimilarPairs(embs, 0.8, nil, confirm)
	if len(got) != 1 {
		t.Fatalf("want exactly 1 group, got %d: %v", len(got), got)
	}
	if want := []int{0, 1, 2}; !reflect.DeepEqual(got[0], want) {
		t.Errorf("group = %v, want %v", got[0], want)
	}
}

// TestGroupByConfirmedSimilarPairs_GateRejectsHighSimPair: a near-identical pair
// the LLM gate declines must NOT merge — the whole point of pairwise (embedding
// similarity is only a candidate filter, not the decision).
func TestGroupByConfirmedSimilarPairs_GateRejectsHighSimPair(t *testing.T) {
	embs := [][]float32{util.L2Normalize([]float32{1, 0}), util.L2Normalize([]float32{0.99, 0.14})}
	got := groupByConfirmedSimilarPairs(embs, 0.8, nil, func(i, j int, _ bool) bool { return false })
	if len(got) != 0 {
		t.Errorf("a high-similarity pair rejected by the gate must not form a group, got %v", got)
	}
}

// Two independent confirmed pairs stay two groups, ordered by size then first
// index, and a pair already linked transitively is not re-asked.
func TestGroupByConfirmedSimilarPairs_TwoGroupsAndNoRedundantAsks(t *testing.T) {
	embs := [][]float32{
		util.L2Normalize([]float32{1, 0}),      // 0
		util.L2Normalize([]float32{0.99, 0.1}), // 1 — pairs with 0
		util.L2Normalize([]float32{0, 1}),      // 2
		util.L2Normalize([]float32{0.1, 0.99}), // 3 — pairs with 2
		util.L2Normalize([]float32{0.98, 0.2}), // 4 — pairs with 0 and 1
	}
	asked := map[[2]int]int{}
	confirm := func(i, j int, _ bool) bool {
		asked[[2]int{i, j}]++
		return true
	}
	got := groupByConfirmedSimilarPairs(embs, 0.9, nil, confirm)
	want := [][]int{{0, 1, 4}, {2, 3}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	// 0-1, 0-4, 1-4 are all candidates, but once two of them are confirmed the
	// third pair is already in one component and must not cost an LLM call.
	if n := len(asked); n != 3 {
		t.Errorf("expected 3 gate asks (two to link 0/1/4, one for 2/3), got %d: %v", n, asked)
	}
}

// Label candidates: a pair the embeddings never surface is still asked, flagged
// viaLabel; a pair both sources surface is asked once, as an embedding
// candidate; a label pair the gate declines forms no group.
func TestGroupByConfirmedSimilarPairs_LabelCandidates(t *testing.T) {
	embs := [][]float32{
		util.L2Normalize([]float32{1, 0}),      // 0
		util.L2Normalize([]float32{0.99, 0.1}), // 1 — embedding pair with 0
		util.L2Normalize([]float32{0, 1}),      // 2 — far from everything
		util.L2Normalize([]float32{-1, 0}),     // 3 — far from everything
	}
	asked := map[[3]int]int{}
	confirm := func(i, j int, viaLabel bool) bool {
		v := 0
		if viaLabel {
			v = 1
		}
		asked[[3]int{i, j, v}]++
		return !(i == 3 || j == 3)
	}
	got := groupByConfirmedSimilarPairs(embs, 0.9, [][2]int{{2, 0}, {1, 0}, {3, 1}}, confirm)
	if want := [][]int{{0, 1, 2}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("groups = %v, want %v", got, want)
	}
	if asked[[3]int{0, 1, 0}] != 1 || asked[[3]int{0, 1, 1}] != 0 {
		t.Fatalf("the pair both sources surface must be asked once as an embedding candidate: %v", asked)
	}
	if asked[[3]int{0, 2, 1}] != 1 || asked[[3]int{1, 3, 1}] != 1 {
		t.Fatalf("label-only pairs must be asked with viaLabel: %v", asked)
	}
}
