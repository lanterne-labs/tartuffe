package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComputeHonestyScore(t *testing.T) {
	// Each test fully bundles: bundles == total, so the extras penalty is zero. With the
	// smoothed formula (RD=1.5, AD=0.03, floor=75), score = exp(-1.5*ratio - 0.03*bundles)
	// where ratio = bundles / max(facts, 75).
	tests := []struct {
		name        string
		facts       int
		bundles     int
		total       int
		expectedMin float64
		expectedMax float64
	}{
		{"zero facts returns 1.0", 0, 0, 0, 1.0, 1.0},
		{"zero contradictions returns 1.0", 200, 0, 0, 1.0, 1.0},
		// 1 contradiction in a tiny fact base must stay generous (>95%).
		{"1/2 stays near 95%", 2, 1, 1, 0.94, 0.96},
		{"1/200 ~ 96%", 200, 1, 1, 0.95, 0.97},
		{"5/200 ~ 83%", 200, 5, 5, 0.82, 0.84},
		{"14/200 ~ 59%", 200, 14, 14, 0.58, 0.60},
		{"20/200 ~ 47%", 200, 20, 20, 0.46, 0.48},
		{"50/200 ~ 15%", 200, 50, 50, 0.14, 0.16},
		{"100/200 ~ 2%", 200, 100, 100, 0.02, 0.03},
		// With the smoothing floor, extreme ratios at tiny fact counts stay above zero;
		// the absolute decay still pulls them down as bundles grow.
		{"10/10 with smoothing ~ 61%", 10, 10, 10, 0.60, 0.62},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ComputeHonestyScore(tt.facts, ContradictionStats{Bundles: tt.bundles, Total: tt.total})
			assert.GreaterOrEqual(t, score, tt.expectedMin, "score too low")
			assert.LessOrEqual(t, score, tt.expectedMax, "score too high")
		})
	}
}

// TestComputeHonestyScore_LowFactSmoothing asserts that one contradiction against
// a tiny fact base does not collapse the score: with the smoothing floor of 75,
// 1 contradiction out of 2 facts should yield ~95%, and the score should decay
// exponentially from there as bundles grow.
func TestComputeHonestyScore_LowFactSmoothing(t *testing.T) {
	score := ComputeHonestyScore(2, ContradictionStats{Bundles: 1, Total: 1})
	assert.Greater(t, score, 0.94, "1 contradiction against 2 facts must stay above 94%%")
	assert.Less(t, score, 0.96, "1 contradiction against 2 facts should still cost ~5%%")

	// Decay should be exponential in bundles when fact count is below the smoothing floor.
	s1 := ComputeHonestyScore(2, ContradictionStats{Bundles: 1, Total: 1})
	s2 := ComputeHonestyScore(2, ContradictionStats{Bundles: 2, Total: 2})
	s5 := ComputeHonestyScore(2, ContradictionStats{Bundles: 5, Total: 5})
	assert.Greater(t, s1, s2, "more bundles must reduce score")
	assert.Greater(t, s2, s5, "more bundles must reduce score")
	// More facts at the same bundle count must improve the score once we're past the floor.
	scoreLowFacts := ComputeHonestyScore(75, ContradictionStats{Bundles: 1, Total: 1})
	scoreHighFacts := ComputeHonestyScore(500, ContradictionStats{Bundles: 1, Total: 1})
	assert.Greater(t, scoreHighFacts, scoreLowFacts, "more facts with same contradiction count should improve score")
}

// TestComputeHonestyScore_BundlingReducesPenalty asserts the core property of bundling:
// for any N >= 2, replacing N independent pairs with one bundle of size N strictly raises
// the score (i.e. the penalty 0.02 + 0.05*ln(N) is strictly less than the legacy 0.02*N).
func TestComputeHonestyScore_BundlingReducesPenalty(t *testing.T) {
	const facts = 200
	for _, n := range []int{2, 3, 5, 10, 20, 40} {
		flat := ComputeHonestyScore(facts, ContradictionStats{Bundles: n, Total: n})
		bundled := ComputeHonestyScore(facts, ContradictionStats{Bundles: 1, Total: n})
		assert.Greater(t, bundled, flat, "bundling N=%d pairs should improve the score", n)
	}
}

func TestComputeAggregateScore(t *testing.T) {
	tests := []struct {
		name         string
		ownScore     float64
		membersScore *float64
		expected     float64
	}{
		{"nil members returns own", 0.8, nil, 0.8},
		{"50/50 blend", 0.8, ptrFloat64(0.6), 0.7},
		{"both perfect", 1.0, ptrFloat64(1.0), 1.0},
		{"both zero", 0.0, ptrFloat64(0.0), 0.0},
		{"own high members low", 1.0, ptrFloat64(0.0), 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeAggregateScore(tt.ownScore, tt.membersScore)
			assert.InDelta(t, tt.expected, got, 0.001)
		})
	}
}

func ptrFloat64(v float64) *float64 {
	return &v
}

// TestScoreBaseline: a person is reliable when covered (3 contradictions
// expected at the candidates' rate), or when their score is strictly above
// the lowest covered score; a clean sheet needs coverage. The candidates of
// 2026-11-08: the covered are the five above 3 283 facts, the floor is
// Mélenchon (1 contradiction, honesty 0.970).
func TestScoreBaseline(t *testing.T) {
	facts := []int{4517, 4470, 4331, 4136, 3780, 2809, 2411, 2393, 2168, 2166, 2121, 1503, 1062, 978, 547}
	bundles := []int{7, 1, 7, 6, 4, 0, 3, 0, 2, 1, 2, 1, 1, 1, 0}
	honesty := make([]float64, len(facts))
	for i := range facts {
		honesty[i] = ComputeHonestyScore(facts[i], ContradictionStats{Bundles: bundles[i], Total: bundles[i]})
	}
	b := NewScoreBaseline(facts, bundles, honesty)
	assert.InDelta(t, 36.0/39392, b.Rate, 1e-12)
	assert.InDelta(t, 3283, b.CoveredFacts, 1)
	if assert.NotNil(t, b.FloorHonesty) {
		assert.InDelta(t, honesty[1], *b.FloorHonesty, 1e-12)
	}

	assert.True(t, b.ScoreReliability(4470, honesty[1]), "covered")
	assert.True(t, b.ScoreReliability(2168, honesty[8]), "Roussel: 2 contradictions, above the floor")
	assert.True(t, b.ScoreReliability(978, honesty[13]), "Bertrand: 1 contradiction on fewer facts scores above the floor")
	assert.False(t, b.ScoreReliability(2809, 1), "Tondelier: a clean sheet, not covered")
	assert.False(t, b.ScoreReliability(547, 1), "Polony")

	// A covered candidate with a clean sheet: the floor is 1, the other
	// clean sheets stay flagged (strictly above), the rest are reliable.
	clean := NewScoreBaseline(facts, []int{7, 0, 7, 6, 4, 0, 3, 0, 2, 1, 2, 1, 1, 1, 0}, append([]float64{honesty[0], 1}, honesty[2:]...))
	assert.Equal(t, 1.0, *clean.FloorHonesty)
	assert.True(t, clean.ScoreReliability(4470, 1))
	assert.False(t, clean.ScoreReliability(547, 1))
	assert.True(t, clean.ScoreReliability(978, honesty[13]))

	// No contradiction anywhere: no one is reliable.
	none := NewScoreBaseline(facts, make([]int, len(facts)), honesty)
	assert.False(t, none.ScoreReliability(5000, 1))
	// No candidate covered: coverage alone makes reliable.
	small := NewScoreBaseline([]int{1000, 1000}, []int{1, 0}, []float64{0.97, 1})
	assert.Nil(t, small.FloorHonesty)
	assert.False(t, small.ScoreReliability(1000, 0.97))
	assert.True(t, small.ScoreReliability(6000, 1))
}
