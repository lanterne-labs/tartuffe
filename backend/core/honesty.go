package core

import "math"

// HonestyScoreDecay sets the scale of the score: honesty = exp(-HonestyScoreDecay * x),
// where x compares the person's contradictions with those expected of their facts
// (HonestyExcess). A well covered person at the candidates' average rate has x near 1: a
// Tartuffe-Score of 45; at twice the average, 70. One contradiction costs at most 18 points.
const HonestyScoreDecay = 0.6

// HonestyScoreSmoothing is added to the expected contradictions: with few facts almost
// none is expected, and one found must not read as many times the average. It is the
// expectation from which a person is covered (ScoreReliableExpected): under it the score
// follows the count of the contradictions, far above it their rate against the average.
const HonestyScoreSmoothing = ScoreReliableExpected

// HonestyScoreExtraWeight is what the repeated pairs inside the bundles add to the distinct
// contradictions: HonestyScoreExtraWeight * ln(1 + extras), extras = pairs - bundles. It
// must stay under 1 / ln 2 (1.4427) so that a bundle of N >= 2 pairs always counts strictly
// less than N distinct bundles: 1 + w*ln(N) < N, binding at N = 2.
const HonestyScoreExtraWeight = 1.4

// ContradictionStats summarises a person's contradictions after stance bundling.
// Bundles is the number of distinct stance-grouped contradictions; Total is the raw pair count.
// Total >= Bundles always; extras := Total - Bundles is the repetition penalty input.
type ContradictionStats struct {
	Bundles int
	Total   int
}

// Weighted is the contradictions as the score counts them: the distinct bundles, plus a
// little for the repeated pairs inside them. Bundling collapses the raw pairs that share a
// stance into one inconsistency, so 10 paraphrase pairs about the same broken promise
// count as one bundle and a small repetition, not as 10 contradictions.
func (c ContradictionStats) Weighted() float64 {
	return float64(c.Bundles) + HonestyScoreExtraWeight*math.Log1p(float64(max(c.Total-c.Bundles, 0)))
}

// HonestyExcess compares a person's contradictions with those expected of their facts at
// rate, the candidates' average contradictions per fact (ScoreBaseline.Rate):
// weighted / (rate * facts + HonestyScoreSmoothing). No constant counts facts: when
// everyone's facts and contradictions grow together, the rate holds and so does the ratio.
// With few facts it is the count that speaks (enough contradictions show incoherence
// whatever was said); with many, the rate against the average (whoever speaks a lot ends
// up contradicting themselves, as the others do). A rate of 0 (no baseline yet) leaves the
// count alone.
func HonestyExcess(factsCount int, stats ContradictionStats, rate float64) float64 {
	return stats.Weighted() / (rate*float64(factsCount) + HonestyScoreSmoothing)
}

// ComputeHonestyScore returns exp(-HonestyScoreDecay * HonestyExcess(facts, stats, rate)).
// If factsCount is 0, returns 1.0 (benefit of the doubt).
func ComputeHonestyScore(factsCount int, stats ContradictionStats, rate float64) float64 {
	if factsCount == 0 {
		return 1.0
	}
	return math.Exp(-HonestyScoreDecay * HonestyExcess(factsCount, stats, rate))
}

// ComputeAggregateScore returns the aggregate honesty score for an entity.
// If membersScore is nil (no members), returns ownScore directly.
// Otherwise returns 0.5*ownScore + 0.5*membersScore.
func ComputeAggregateScore(ownScore float64, membersScore *float64) float64 {
	if membersScore == nil {
		return ownScore
	}
	return 0.5*ownScore + 0.5*(*membersScore)
}

// ScoreReliableExpected is the number of contradictions a person's facts
// must be expected to hold, at the candidates' average rate, for the
// person to be covered: below it, finding none by chance is likelier than
// 5 % (Poisson: e^-3), so a clean score says little.
const ScoreReliableExpected = 3.0

// ScoreBaseline is what every person's score is measured against: the
// candidates' contradictions per fact, the facts from which a person is
// covered (ScoreReliableExpected at that rate), and the highest honesty
// (the lowest Tartuffe-Score) among the covered candidates. Both move with
// the candidates' facts and contradictions.
type ScoreBaseline struct {
	Rate         float64
	CoveredFacts float64
	// FloorHonesty is the honesty of the most coherent covered candidate;
	// nil when none is covered.
	FloorHonesty *float64
}

// NewScoreBaseline measures the baseline from each candidate's facts,
// distinct contradictions and honesty score. With no contradiction
// anywhere no one is covered.
func NewScoreBaseline(facts, bundles []int, honesty []float64) ScoreBaseline {
	var sumF, sumB float64
	for i := range facts {
		sumF += float64(facts[i])
		sumB += float64(bundles[i])
	}
	if sumF == 0 || sumB == 0 {
		return ScoreBaseline{}
	}
	b := ScoreBaseline{Rate: sumB / sumF}
	b.CoveredFacts = ScoreReliableExpected / b.Rate
	for i := range facts {
		if float64(facts[i]) >= b.CoveredFacts && (b.FloorHonesty == nil || honesty[i] > *b.FloorHonesty) {
			h := honesty[i]
			b.FloorHonesty = &h
		}
	}
	return b
}

// ScoreReliability says whether a score built on facts is reliable. The
// detection misses contradictions far more than it invents them, so a
// score is far more often too low than too high: a person is reliable when
// covered (enough facts for a clean score to mean something), or when
// their score is already strictly above the lowest score of the covered:
// with so few facts it is the count of their contradictions that put them
// there, and more facts bring more of them. A clean sheet needs coverage.
func (b ScoreBaseline) ScoreReliability(facts int, honesty float64) bool {
	if b.CoveredFacts <= 0 {
		return false
	}
	if float64(facts) >= b.CoveredFacts {
		return true
	}
	return b.FloorHonesty != nil && honesty < *b.FloorHonesty
}
