package cmd

// dotProductF32 returns the dot product of two equal-length float32 vectors,
// which equals cosine similarity when both are L2-normalised (as stance cluster
// embeddings are). Returns 0 on a length mismatch or empty input. Used by the
// pairwise stance grouping to find embedding-similar candidate pairs.
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
