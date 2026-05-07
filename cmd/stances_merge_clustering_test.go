package cmd

import (
	"math"
	"testing"
)

// unitAxis returns a length-N L2-normalised float32 vector with value 1.0 at
// index `axis` (and zero elsewhere). Useful for synthetic centroids that we
// can reason about by inner-product geometry. (Named to avoid colliding with
// `unitVec` in stances_backfill_test.go which has different parameter order.)
func unitAxis(axis, dim int) []float32 {
	v := make([]float32, dim)
	v[axis] = 1
	return v
}

// blendAxes returns the L2-normalised mix of two unit axes at the given weights.
func blendAxes(axisA, axisB int, wA, wB float64, dim int) []float32 {
	v := make([]float32, dim)
	v[axisA] = float32(wA)
	v[axisB] = float32(wB)
	norm := math.Sqrt(wA*wA + wB*wB)
	v[axisA] = float32(wA / norm)
	v[axisB] = float32(wB / norm)
	return v
}

// almostEqualSet checks that the cluster lists match as sets of sets,
// ignoring per-cluster order and overall order.
func almostEqualSet(got, want [][]int) bool {
	if len(got) != len(want) {
		return false
	}
	used := make([]bool, len(want))
outer:
	for _, g := range got {
		for j, w := range want {
			if used[j] {
				continue
			}
			if intSliceEqual(g, w) {
				used[j] = true
				continue outer
			}
		}
		return false
	}
	return true
}

func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestAgglomerativeAverageLinkage_AllSeparate(t *testing.T) {
	// Four orthogonal centroids: every pair has distance 1. With cutoff 0.5
	// nothing should merge — output is four singletons.
	dim := 4
	centroids := [][]float32{
		unitAxis(0, dim),
		unitAxis(1, dim),
		unitAxis(2, dim),
		unitAxis(3, dim),
	}
	got := agglomerativeAverageLinkage(centroids, 0.5)
	want := [][]int{{0}, {1}, {2}, {3}}
	if !almostEqualSet(got, want) {
		t.Fatalf("expected 4 singletons, got %v", got)
	}
}

func TestAgglomerativeAverageLinkage_TightCluster(t *testing.T) {
	// Three near-identical points (sim ≈ 0.999) plus one orthogonal outlier.
	// At cutoff 0.35 the three near-identical merge, the outlier stays alone.
	dim := 4
	a := blendAxes(0, 1, 0.99, 0.14, dim)
	b := blendAxes(0, 1, 0.97, 0.24, dim)
	c := blendAxes(0, 1, 0.98, 0.20, dim)
	d := unitAxis(2, dim)
	centroids := [][]float32{a, b, c, d}
	got := agglomerativeAverageLinkage(centroids, 0.35)
	want := [][]int{{0, 1, 2}, {3}}
	if !almostEqualSet(got, want) {
		t.Fatalf("expected {0,1,2} and {3}, got %v", got)
	}
}

func TestAgglomerativeAverageLinkage_NoChaining(t *testing.T) {
	// Single-link "chaining" trap: A-B and B-C are close, but A-C is far.
	// With single-link UnionFind they'd all merge transitively. With average
	// linkage at a tight cutoff, the average-distance from C to {A,B} blocks
	// the third merge.
	//
	// Construct on the unit circle:
	//   A at angle 0       (cos=1.000)
	//   B at angle 30°     (cos to A = 0.866, dist=0.134)
	//   C at angle 60°     (cos to B = 0.866, cos to A = 0.500)
	//
	// With cutoff 0.20: A-B merges (d=0.134). Now C has avg dist to {A,B}
	// of (0.500 + 0.134)/2 = 0.317 (using distances 1-cos), well above 0.20.
	// So C stays a singleton.
	dim := 2
	deg := math.Pi / 180.0
	pt := func(theta float64) []float32 {
		v := make([]float32, dim)
		v[0] = float32(math.Cos(theta))
		v[1] = float32(math.Sin(theta))
		return v
	}
	a := pt(0)
	b := pt(30 * deg)
	c := pt(60 * deg)
	got := agglomerativeAverageLinkage([][]float32{a, b, c}, 0.20)
	want := [][]int{{0, 1}, {2}}
	if !almostEqualSet(got, want) {
		t.Fatalf("expected {0,1} and {2} (no chain), got %v", got)
	}
}

func TestAgglomerativeAverageLinkage_Singleton(t *testing.T) {
	got := agglomerativeAverageLinkage([][]float32{unitAxis(0, 4)}, 0.5)
	want := [][]int{{0}}
	if !almostEqualSet(got, want) {
		t.Fatalf("expected single singleton, got %v", got)
	}
}

func TestAgglomerativeAverageLinkage_Empty(t *testing.T) {
	got := agglomerativeAverageLinkage(nil, 0.5)
	if got != nil {
		t.Fatalf("expected nil for empty input, got %v", got)
	}
}

func TestAgglomerativeAverageLinkage_LooseCutoffMergesAll(t *testing.T) {
	// Cutoff 1.5 (above max possible cosine distance of 1.0) merges everything.
	dim := 4
	centroids := [][]float32{
		unitAxis(0, dim),
		unitAxis(1, dim),
		unitAxis(2, dim),
		unitAxis(3, dim),
	}
	got := agglomerativeAverageLinkage(centroids, 1.5)
	want := [][]int{{0, 1, 2, 3}}
	if !almostEqualSet(got, want) {
		t.Fatalf("expected one all-inclusive cluster, got %v", got)
	}
}

func TestAgglomerativeAverageLinkage_Deterministic(t *testing.T) {
	dim := 4
	centroids := [][]float32{
		blendAxes(0, 1, 1.0, 0.05, dim),
		blendAxes(0, 1, 1.0, 0.06, dim),
		blendAxes(0, 1, 0.05, 1.0, dim),
		blendAxes(0, 1, 0.06, 1.0, dim),
	}
	first := agglomerativeAverageLinkage(centroids, 0.35)
	for i := 0; i < 5; i++ {
		got := agglomerativeAverageLinkage(centroids, 0.35)
		if !almostEqualSet(got, first) {
			t.Fatalf("non-deterministic clustering: run %d gave %v, want %v", i, got, first)
		}
	}
}
