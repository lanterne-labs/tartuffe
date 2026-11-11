package core

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

// testRate is the candidates' rate of 2026-11-11: about 0.78 contradictions per 1 000 facts.
const testRate = 0.00078

func TestComputeHonestyScore(t *testing.T) {
	// Each test fully bundles: bundles == total, so the repeats add nothing and
	// score = exp(-0.6 * bundles / (rate * facts + 3)).
	tests := []struct {
		name        string
		facts       int
		bundles     int
		expectedMin float64
		expectedMax float64
	}{
		{"zero facts returns 1.0", 0, 0, 1.0, 1.0},
		{"zero contradictions returns 1.0", 200, 0, 1.0, 1.0},
		// With few facts, one contradiction costs 18 points at most.
		{"1/2 ~ 82%", 2, 1, 0.81, 0.82},
		{"1/1000 ~ 85%", 1000, 1, 0.85, 0.86},
		{"1/5000 ~ 92%", 5000, 1, 0.91, 0.92},
		// The same 15 contradictions weigh less and less as the facts grow.
		{"15/300 ~ 6%", 300, 15, 0.06, 0.07},
		{"15/1000 ~ 9%", 1000, 15, 0.09, 0.10},
		{"15/5000 ~ 27%", 5000, 15, 0.27, 0.28},
		{"15/20000 ~ 62%", 20000, 15, 0.61, 0.62},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ComputeHonestyScore(tt.facts, ContradictionStats{Bundles: tt.bundles, Total: tt.bundles}, testRate)
			assert.GreaterOrEqual(t, score, tt.expectedMin, "score too low")
			assert.LessOrEqual(t, score, tt.expectedMax, "score too high")
		})
	}
}

// TestComputeHonestyScore_CountThenRate asserts what the ratio to the expected
// contradictions holds: the count with few facts, the rate against the average with many,
// and the same scores when everything grows together.
func TestComputeHonestyScore_CountThenRate(t *testing.T) {
	score := func(facts, bundles int, rate float64) float64 {
		return ComputeHonestyScore(facts, ContradictionStats{Bundles: bundles, Total: bundles}, rate)
	}
	// The count matters: at any fact count, more contradictions lower the score.
	for _, facts := range []int{2, 500, 3000, 200000} {
		assert.Greater(t, score(facts, 1, testRate), score(facts, 2, testRate), "%d facts", facts)
		assert.Greater(t, score(facts, 2, testRate), score(facts, 5, testRate), "%d facts", facts)
	}
	// The facts matter: the same contradictions among more facts cost less.
	assert.Greater(t, score(1000, 15, testRate), score(300, 15, testRate))
	assert.Greater(t, score(5000, 15, testRate), score(1000, 15, testRate))
	// With few facts the count speaks: the same rate over more facts costs more.
	assert.Greater(t, score(500, 2, testRate), score(2000, 8, testRate))
	// A fact that comes with its contradiction never raises the score.
	assert.Greater(t, score(3000, 5, testRate), score(3001, 6, testRate))

	// Well covered, at the average rate: the excess nears 1 whatever the size.
	at := func(facts int) float64 {
		return HonestyExcess(facts, ContradictionStats{Bundles: int(testRate * float64(facts)), Total: int(testRate * float64(facts))}, testRate)
	}
	assert.InDelta(t, 1.0, at(1_000_000), 0.01)
	assert.InDelta(t, 1.0, at(10_000_000), 0.001)
	// Ten times the facts and the contradictions of everyone: the rate holds, and a covered
	// person's score moves by a few points, towards what their rate says.
	assert.InDelta(t, score(40_000, 60, testRate), score(400_000, 600, testRate), 0.04)
	assert.InDelta(t, score(400_000, 600, testRate), score(4_000_000, 6000, testRate), 0.005)
	// Detection finds twice as much for everyone: a covered person does not move much.
	assert.InDelta(t, score(40_000, 60, testRate), score(40_000, 120, 2*testRate), 0.03)
	// No baseline: the count alone.
	assert.Equal(t, score(10, 3, 0), score(10_000, 3, 0))
	assert.InDelta(t, 1.0, HonestyExcess(10, ContradictionStats{Bundles: 3, Total: 3}, 0), 1e-12)
}

// TestComputeHonestyScore_BundlingReducesPenalty asserts the core property of bundling:
// for any N >= 2, replacing N independent pairs with one bundle of size N strictly raises
// the score (1 + w*ln(N) < N). The condition on the constant is w < 1 / ln 2 (the case
// N = 2, the tightest).
func TestComputeHonestyScore_BundlingReducesPenalty(t *testing.T) {
	assert.Less(t, HonestyScoreExtraWeight*math.Ln2, 1.0,
		"a bundle of 2 pairs must count less than 2 distinct bundles")
	for _, facts := range []int{200, 5000} {
		for _, n := range []int{2, 3, 5, 10, 20, 40} {
			flat := ComputeHonestyScore(facts, ContradictionStats{Bundles: n, Total: n}, testRate)
			bundled := ComputeHonestyScore(facts, ContradictionStats{Bundles: 1, Total: n}, testRate)
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
		honesty[i] = ComputeHonestyScore(facts[i], ContradictionStats{Bundles: bundles[i], Total: bundles[i]}, 36.0/39392)
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
