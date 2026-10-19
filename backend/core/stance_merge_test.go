package core

import (
	"digital-mirror/backend/util"
	"reflect"
	"strings"
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

// TestSameSubjectLookupPrompt pins the layout: rules, then the person's
// stances (shared by every lookup of the person), then the small stance, and
// the answer's id after its reasoning.
func TestSameSubjectLookupPrompt(t *testing.T) {
	others := []StanceDocument{{ID: 18881, ClusterLabel: "politique française sur la guerre en Ukraine", FactCount: 53}}
	p := sameSubjectLookupPrompt("marine le pen", StanceForMerge{ID: 21882, Label: "guerre en Ukraine et diplomatie", Summary: "s"}, others)
	list := strings.Index(p, "- id=18881 (53 facts)")
	small := strings.Index(p, "SMALL STANCE:")
	person := strings.Index(p, "Person: marine le pen")
	if person < 0 || list < person || small < list {
		t.Fatalf("layout: person %d, list %d, small stance %d\n%s", person, list, small, p)
	}
	if strings.Index(p, `"reasoning"`) > strings.Index(p, `"id": 12345`) {
		t.Error("the reasoning must come before the id")
	}
}

// The lookup fingerprint changes with the small stance and with the set of
// stances it could join, not with their labels or counts.
func TestLookupFingerprint(t *testing.T) {
	small := StanceDocument{ID: 1, ClusterLabel: "guerre en Ukraine et diplomatie", Summary: "s", FactCount: 1}
	big := StanceDocument{ID: 2, ClusterLabel: "politique française sur la guerre en Ukraine", FactCount: 53}
	tiny := StanceDocument{ID: 3, ClusterLabel: "football", FactCount: 1}
	fp := lookupFingerprint("k", small, []StanceDocument{small, big, tiny}, 3)

	refreshed := big
	refreshed.ClusterLabel, refreshed.FactCount = "guerre en Ukraine", 54
	if lookupFingerprint("k", small, []StanceDocument{small, refreshed, tiny}, 3) != fp {
		t.Error("a refreshed label or count of a joinable stance must keep the memo")
	}
	if lookupFingerprint("k", small, []StanceDocument{small, big}, 3) != fp {
		t.Error("a small stance that cannot be joined must not change the memo")
	}
	grown := StanceDocument{ID: 4, ClusterLabel: "diplomatie européenne", FactCount: 9}
	if lookupFingerprint("k", small, []StanceDocument{small, big, grown}, 3) == fp {
		t.Error("a new joinable stance must ask again")
	}
	edited := small
	edited.Summary = "s2"
	if lookupFingerprint("k", edited, []StanceDocument{edited, big}, 3) == fp {
		t.Error("an edited small stance must ask again")
	}
	if lookupFingerprint("k2", small, []StanceDocument{small, big}, 3) == fp {
		t.Error("another rulebook must ask again")
	}
}
