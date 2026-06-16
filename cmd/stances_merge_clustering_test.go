package cmd

import (
	"math"
	"reflect"
	"testing"
)

// normVec L2-normalises a float32 vector so dotProductF32 yields cosine similarity.
func normVec(v []float32) []float32 {
	var s float64
	for _, x := range v {
		s += float64(x) * float64(x)
	}
	s = math.Sqrt(s)
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / s)
	}
	return out
}

// TestGroupByConfirmedSimilarPairs: similar embeddings become candidate pairs,
// the confirm() gate decides, and confirmed pairs union-find into one group.
func TestGroupByConfirmedSimilarPairs(t *testing.T) {
	embs := [][]float32{
		normVec([]float32{1, 0}),       // 0
		normVec([]float32{0.98, 0.2}),  // 1 — close to 0
		normVec([]float32{0.95, 0.31}), // 2 — close to 0 and 1
		normVec([]float32{0, 1}),       // 3 — far from the rest
	}
	// 0,1,2 are the same subject; anything touching 3 is rejected (and 3 isn't
	// even a candidate by similarity).
	confirm := func(i, j int) bool { return i != 3 && j != 3 }
	got := groupByConfirmedSimilarPairs(embs, 0.8, confirm)
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
	embs := [][]float32{normVec([]float32{1, 0}), normVec([]float32{0.99, 0.14})}
	got := groupByConfirmedSimilarPairs(embs, 0.8, func(i, j int) bool { return false })
	if len(got) != 0 {
		t.Errorf("a high-similarity pair rejected by the gate must not form a group, got %v", got)
	}
}
