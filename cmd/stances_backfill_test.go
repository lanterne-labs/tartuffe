package cmd

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// unitVec returns a length-`dim` unit vector with the given dimension hot.
func unitVec(dim, idx int) []float32 {
	v := make([]float32, dim)
	v[idx] = 1
	return v
}

// TestWeightedMergedCentroid: weighted average of two unit-axes with weights
// (3, 7) along axes 0 and 1, post L2-normalisation. Expected direction is
// (3/10, 7/10) then normalised. Used by applyMergeSubgroup when recomputing
// the destination centroid post-merge.
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
