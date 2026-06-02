package core

import (
	"sort"
	"testing"
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
