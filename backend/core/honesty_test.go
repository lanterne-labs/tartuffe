package core

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestComputeHonestyScore(t *testing.T) {
	// Each test fully bundles: bundles == total, so the extras penalty is zero and
	// score = exp(-sqrt(3000/max(facts, 500)) * 0.09 * bundles).
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
		// At the reference, a contradiction has its plain weight.
		{"1/3000 ~ 91%", 3000, 1, 1, 0.91, 0.92},
		{"7/3000 ~ 53%", 3000, 7, 7, 0.53, 0.54},
		// Under the floor the weight is capped: one contradiction costs 20 points.
		{"1/2 ~ 80%", 2, 1, 1, 0.80, 0.81},
		{"1/500 ~ 80%", 500, 1, 1, 0.80, 0.81},
		// The same 15 contradictions weigh less and less as the facts grow, and never nothing.
		{"15/300 ~ 4%", 300, 15, 15, 0.03, 0.04},
		{"15/1000 ~ 10%", 1000, 15, 15, 0.09, 0.10},
		{"15/3000 ~ 26%", 3000, 15, 15, 0.25, 0.26},
		{"15/12000 ~ 51%", 12000, 15, 15, 0.50, 0.51},
		{"60/12000 ~ 7%", 12000, 60, 60, 0.06, 0.07},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ComputeHonestyScore(tt.facts, ContradictionStats{Bundles: tt.bundles, Total: tt.total})
			assert.GreaterOrEqual(t, score, tt.expectedMin, "score too low")
			assert.LessOrEqual(t, score, tt.expectedMax, "score too high")
		})
	}
}

// TestComputeHonestyScore_BetweenCountAndRate asserts the middle the weight holds between
// the count of the contradictions and their rate.
func TestComputeHonestyScore_BetweenCountAndRate(t *testing.T) {
	score := func(facts, bundles int) float64 {
		return ComputeHonestyScore(facts, ContradictionStats{Bundles: bundles, Total: bundles})
	}
	// The count matters: at any fact count, more contradictions lower the score.
	for _, facts := range []int{2, 500, 3000, 20000} {
		assert.Greater(t, score(facts, 1), score(facts, 2), "%d facts", facts)
		assert.Greater(t, score(facts, 2), score(facts, 5), "%d facts", facts)
	}
	// The facts matter: the same contradictions among more facts cost less.
	assert.Greater(t, score(1000, 15), score(500, 15))
	assert.Greater(t, score(5000, 15), score(1000, 15))
	// Not the rate alone: the same rate over more facts is more contradictions, and costs more.
	assert.Greater(t, score(1000, 5), score(4000, 20))
	// Under the floor the facts no longer count: a small sample tells no rate.
	assert.Equal(t, score(2, 1), score(500, 1))
	// 4 times the facts halve the weight.
	assert.InDelta(t, HonestyFactsWeight(1000)/2, HonestyFactsWeight(4000), 1e-12)
	assert.InDelta(t, 1.0, HonestyFactsWeight(HonestyScoreReferenceFacts), 1e-12)
	// A fact that comes with its contradiction never raises the score.
	assert.Greater(t, score(3000, 5), score(3001, 6))
}

// TestComputeHonestyScore_BundlingReducesPenalty asserts the core property of bundling:
// for any N >= 2, replacing N independent pairs with one bundle of size N strictly raises
// the score (i.e. the penalty j + a*ln(N) is strictly less than j*N). The condition on
// the constants is a < j / ln 2 (the case N = 2, the tightest): the facts weigh on both
// sides alike, so the constants alone must hold it.
func TestComputeHonestyScore_BundlingReducesPenalty(t *testing.T) {
	assert.Less(t, HonestyScoreBundleExtraDecay*math.Ln2, HonestyScoreAbsoluteDecay,
		"a bundle of 2 pairs must cost less than 2 distinct bundles whatever the fact count")
	for _, facts := range []int{200, 5000} {
		for _, n := range []int{2, 3, 5, 10, 20, 40} {
			flat := ComputeHonestyScore(facts, ContradictionStats{Bundles: n, Total: n})
			bundled := ComputeHonestyScore(facts, ContradictionStats{Bundles: 1, Total: n})
			assert.Greater(t, bundled, flat, "bundling N=%d pairs should improve the score (%d facts)", n, facts)
		}
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
