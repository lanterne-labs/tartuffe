package cmd

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// unitVec returns a length-3 unit vector with the given dimension hot.
func unitVec(dim, idx int) []float32 {
	v := make([]float32, dim)
	v[idx] = 1
	return v
}

// blendedUnit returns a normalized blend of axes i and j with weights w_i, w_j.
func blendedUnit(dim, i, j int, wi, wj float32) []float32 {
	v := make([]float32, dim)
	v[i] = wi
	v[j] = wj
	norm := float32(math.Sqrt(float64(wi*wi + wj*wj)))
	v[i] /= norm
	v[j] /= norm
	return v
}

// TestMergeSelection_AboveThreshold: two close stances merge, the third sits
// alone. The representative is the one with the larger FactCount.
func TestMergeSelection_AboveThreshold(t *testing.T) {
	a := blendedUnit(8, 0, 1, 0.95, 0.31) // ≈ axis 0
	b := blendedUnit(8, 0, 1, 0.99, 0.14) // also ≈ axis 0; sim(a,b) ≈ 0.97
	c := unitVec(8, 5)                    // orthogonal to both

	in := []stanceMergeInput{
		{ID: 1, FactCount: 2, Centroid: a},
		{ID: 2, FactCount: 5, Centroid: b}, // largest fact count → representative
		{ID: 3, FactCount: 4, Centroid: c},
	}
	steps := computeMergeGroups(in, 0.85)
	require := assert.New(t)
	require.Len(steps, 1)
	require.Equal(int64(1), steps[0].SrcID)
	require.Equal(int64(2), steps[0].DstID)
}

// TestMergeSelection_BelowThreshold: nothing merges when no pair crosses the cut.
func TestMergeSelection_BelowThreshold(t *testing.T) {
	in := []stanceMergeInput{
		{ID: 1, FactCount: 1, Centroid: unitVec(8, 0)},
		{ID: 2, FactCount: 1, Centroid: unitVec(8, 1)},
		{ID: 3, FactCount: 1, Centroid: unitVec(8, 2)},
	}
	steps := computeMergeGroups(in, 0.85)
	assert.Empty(t, steps)
}

// TestMergeSelection_Transitive: A↔B and B↔C are above the threshold but A↔C is
// not — UnionFind should still pull all three into one group.
func TestMergeSelection_Transitive(t *testing.T) {
	// Three vectors arranged so consecutive ones are close, ends are far.
	a := blendedUnit(8, 0, 1, 1.0, 0.0)   // ≈ axis 0
	b := blendedUnit(8, 0, 1, 0.95, 0.31) // sim(a,b) ≈ 0.95
	c := blendedUnit(8, 0, 1, 0.71, 0.71) // sim(b,c) ≈ 0.89, sim(a,c) ≈ 0.71

	in := []stanceMergeInput{
		{ID: 10, FactCount: 1, Centroid: a},
		{ID: 11, FactCount: 9, Centroid: b}, // largest → representative
		{ID: 12, FactCount: 1, Centroid: c},
	}
	steps := computeMergeGroups(in, 0.85)
	require := assert.New(t)
	require.Len(steps, 2, "transitive grouping should fold both A and C into B")
	for _, s := range steps {
		require.Equal(int64(11), s.DstID)
		require.NotEqual(int64(11), s.SrcID)
	}
}

// TestWeightedMergedCentroid: weighted average of two unit-axes with weights
// (3, 7) along axes 0 and 1, post L2-normalisation. Expected direction is
// (3/10, 7/10) then normalised.
func TestWeightedMergedCentroid(t *testing.T) {
	in := []stanceMergeInput{
		{ID: 1, FactCount: 3, Centroid: unitVec(4, 0)},
		{ID: 2, FactCount: 7, Centroid: unitVec(4, 1)},
	}
	out := weightedNormalisedCentroid(in, []int{0, 1})

	wantNorm := math.Sqrt(0.3*0.3 + 0.7*0.7)
	want0 := float32(0.3 / wantNorm)
	want1 := float32(0.7 / wantNorm)

	assert.InDelta(t, want0, out[0], 1e-5)
	assert.InDelta(t, want1, out[1], 1e-5)
	assert.InDelta(t, 0, out[2], 1e-5)
	assert.InDelta(t, 0, out[3], 1e-5)

	var sumSq float64
	for _, v := range out {
		sumSq += float64(v) * float64(v)
	}
	assert.InDelta(t, 1.0, sumSq, 1e-5, "result must be unit-norm")
}
