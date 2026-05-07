package cmd

import (
	"container/heap"
	"sort"
)

// agglomerativeAverageLinkage runs agglomerative hierarchical clustering with
// average linkage on the cosine-distance metric implied by the input centroids.
// Centroids are assumed L2-normalised so cos(a,b) == dot(a,b) and distance ==
// 1 - dot(a,b).
//
// Clusters are merged greedily by smallest pairwise (average-linkage) distance
// until the smallest such distance exceeds distanceCutoff.
//
// Average-linkage distances are maintained incrementally with the
// Lance-Williams update for unweighted average linkage:
//
//	d(A∪B, C) = (|A|·d(A,C) + |B|·d(B,C)) / (|A| + |B|)
//
// Returns one slice per resulting cluster, listing the *input indices* that
// belong to that cluster. Output is deterministic: clusters are sorted by
// (size desc, smallest-member-index asc), and members within each cluster are
// sorted ascending. Singletons (size-1 clusters) are included; the caller
// drops them.
func agglomerativeAverageLinkage(centroids [][]float32, distanceCutoff float64) [][]int {
	n := len(centroids)
	if n == 0 {
		return nil
	}
	if n == 1 {
		return [][]int{{0}}
	}

	active := make([]bool, n)
	size := make([]int, n)
	members := make([][]int, n)
	for i := 0; i < n; i++ {
		active[i] = true
		size[i] = 1
		members[i] = []int{i}
	}

	// Pairwise distances. Only consult dist[i][j] for i<j; diagonal unused.
	dist := make([][]float64, n)
	for i := range dist {
		dist[i] = make([]float64, n)
	}
	pq := &mergePQ{}
	heap.Init(pq)

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			d := 1.0 - dotProductF32(centroids[i], centroids[j])
			dist[i][j] = d
			heap.Push(pq, mergeCandidate{i: i, j: j, d: d, sizeI: 1, sizeJ: 1})
		}
	}

	for pq.Len() > 0 {
		top := heap.Pop(pq).(mergeCandidate)
		// Stale entry filter: cluster gone, or sizes have shifted since push.
		if !active[top.i] || !active[top.j] {
			continue
		}
		if size[top.i] != top.sizeI || size[top.j] != top.sizeJ {
			continue
		}
		if top.d > distanceCutoff {
			break
		}

		// Merge top.j into top.i. Capture pre-merge sizes for Lance-Williams.
		a, b := top.i, top.j
		sizeA, sizeB := size[a], size[b]
		newSize := sizeA + sizeB
		members[a] = append(members[a], members[b]...)
		size[a] = newSize
		active[b] = false
		members[b] = nil

		// Lance-Williams update for every other live cluster c.
		for c := 0; c < n; c++ {
			if c == a || c == b || !active[c] {
				continue
			}
			dac := lookupDist(dist, a, c)
			dbc := lookupDist(dist, b, c)
			newD := (float64(sizeA)*dac + float64(sizeB)*dbc) / float64(newSize)
			storeDist(dist, a, c, newD)
			lo, hi := a, c
			if lo > hi {
				lo, hi = hi, lo
			}
			heap.Push(pq, mergeCandidate{
				i: lo, j: hi, d: newD,
				sizeI: size[lo], sizeJ: size[hi],
			})
		}
	}

	var clusters [][]int
	for i := 0; i < n; i++ {
		if !active[i] {
			continue
		}
		m := append([]int(nil), members[i]...)
		sort.Ints(m)
		clusters = append(clusters, m)
	}

	sort.SliceStable(clusters, func(a, b int) bool {
		if len(clusters[a]) != len(clusters[b]) {
			return len(clusters[a]) > len(clusters[b])
		}
		return clusters[a][0] < clusters[b][0]
	})
	return clusters
}

func lookupDist(dist [][]float64, i, j int) float64 {
	if i == j {
		return 0
	}
	if i < j {
		return dist[i][j]
	}
	return dist[j][i]
}

func storeDist(dist [][]float64, i, j int, d float64) {
	if i == j {
		return
	}
	if i < j {
		dist[i][j] = d
	} else {
		dist[j][i] = d
	}
}

func dotProductF32(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// mergeCandidate is one entry in the priority queue: (i, j) is a pair of
// live clusters with i<j, d is their average-linkage distance, and sizeI/sizeJ
// are the cluster sizes at push time. When popped, if either cluster has
// changed size (or been deactivated), the entry is stale and skipped — the
// real distance has since been updated by Lance-Williams and a fresh entry
// pushed.
type mergeCandidate struct {
	i, j         int
	d            float64
	sizeI, sizeJ int
}

type mergePQ []mergeCandidate

func (p mergePQ) Len() int { return len(p) }
func (p mergePQ) Less(a, b int) bool {
	if p[a].d != p[b].d {
		return p[a].d < p[b].d
	}
	if p[a].i != p[b].i {
		return p[a].i < p[b].i
	}
	return p[a].j < p[b].j
}
func (p mergePQ) Swap(a, b int)       { p[a], p[b] = p[b], p[a] }
func (p *mergePQ) Push(x interface{}) { *p = append(*p, x.(mergeCandidate)) }
func (p *mergePQ) Pop() interface{} {
	old := *p
	n := len(old)
	x := old[n-1]
	*p = old[:n-1]
	return x
}
