package util

import "math"

// DotProductF32 returns the dot product of two equal-length float32 vectors,
// which equals cosine similarity when both are L2-normalised (as stance cluster
// embeddings and fact embeddings are). Returns 0 on a length mismatch or empty
// input.
func DotProductF32(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// CosineDistance returns 1 - cosine_similarity. Fact embeddings are stored
// L2-normalised (verified: <#> self-product = -1), so the dot product IS the
// cosine similarity.
func CosineDistance(a, b []float32) float64 {
	return 1.0 - DotProductF32(a, b)
}

// L2Normalize returns a copy of v scaled to unit length. A zero vector is
// returned unchanged (copied).
func L2Normalize(v []float32) []float32 {
	out := make([]float32, len(v))
	var sumSq float64
	for _, x := range v {
		sumSq += float64(x) * float64(x)
	}
	norm := math.Sqrt(sumSq)
	for i, x := range v {
		if norm > 0 {
			out[i] = float32(float64(x) / norm)
		} else {
			out[i] = x
		}
	}
	return out
}

// WeightedNormalisedCentroid returns Σ(vectors_i × weights_i) / Σweights_i,
// then L2-normalised. A weight <= 0 counts as 1. The dimension is taken from
// the first vector. Returns nil when there are no vectors. Used as the new
// centroid of the destination stance after a merge.
func WeightedNormalisedCentroid(vectors [][]float32, weights []int) []float32 {
	if len(vectors) == 0 {
		return nil
	}
	dim := len(vectors[0])
	acc := make([]float64, dim)
	var totalWeight float64
	for i, vec := range vectors {
		w := float64(weights[i])
		if w <= 0 {
			w = 1
		}
		for j, v := range vec {
			acc[j] += float64(v) * w
		}
		totalWeight += w
	}
	out := make([]float32, dim)
	var sumSq float64
	for i := range acc {
		v := acc[i] / totalWeight
		out[i] = float32(v)
		sumSq += v * v
	}
	norm := math.Sqrt(sumSq)
	if norm > 0 {
		for i := range out {
			out[i] = float32(float64(out[i]) / norm)
		}
	}
	return out
}
