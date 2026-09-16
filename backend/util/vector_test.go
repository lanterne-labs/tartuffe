package util

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

// TestWeightedNormalisedCentroid: weighted average of two unit-axes with weights
// (3, 7) along axes 0 and 1, post L2-normalisation. Expected direction is
// (3/10, 7/10) then normalised.
func TestWeightedNormalisedCentroid(t *testing.T) {
	out := WeightedNormalisedCentroid([][]float32{unitVec(4, 0), unitVec(4, 1)}, []int{3, 7})

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

// A non-positive weight counts as 1, and no vectors yields nil.
func TestWeightedNormalisedCentroid_Edges(t *testing.T) {
	assert.Nil(t, WeightedNormalisedCentroid(nil, nil))

	out := WeightedNormalisedCentroid([][]float32{unitVec(2, 0), unitVec(2, 1)}, []int{0, 1})
	assert.InDelta(t, out[0], out[1], 1e-6, "zero weight must count as 1, giving an equal mix")
}

func TestDotProductAndCosineDistance(t *testing.T) {
	a := L2Normalize([]float32{1, 0})
	b := L2Normalize([]float32{1, 1})
	assert.InDelta(t, 1/math.Sqrt2, DotProductF32(a, b), 1e-6)
	assert.InDelta(t, 1-1/math.Sqrt2, CosineDistance(a, b), 1e-6)

	assert.Equal(t, 0.0, DotProductF32([]float32{1}, []float32{1, 2}), "length mismatch yields 0")
	assert.Equal(t, 0.0, DotProductF32(nil, nil), "empty input yields 0")
}

func TestL2Normalize(t *testing.T) {
	v := L2Normalize([]float32{3, 4})
	assert.InDelta(t, 0.6, v[0], 1e-6)
	assert.InDelta(t, 0.8, v[1], 1e-6)
	assert.Equal(t, []float32{0, 0}, L2Normalize([]float32{0, 0}), "zero vector is returned unchanged")
}
