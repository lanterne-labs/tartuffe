package core

import "math"

// HonestyScoreAbsoluteDecay is the penalty of one distinct contradiction bundle for a person
// with HonestyScoreReferenceFacts facts: about 9 points of Tartuffe-Score. Each bundle
// contributes one full penalty regardless of how many raw pairs it contains.
const HonestyScoreAbsoluteDecay = 0.09

// HonestyScoreBundleExtraDecay controls the (logarithmic) penalty for repeated contradictions
// inside the same bundle. extras = total raw pairs - distinct bundles. It must stay under
// HonestyScoreAbsoluteDecay / ln 2 (0.1298) so that a bundle of N >= 2 pairs always costs
// strictly less than N distinct bundles: j + a*ln(N) < j*N, binding at N = 2.
const HonestyScoreBundleExtraDecay = 0.125

// HonestyScoreReferenceFacts is the number of facts at which a contradiction has its plain
// weight (the constants above). With more facts it weighs less, with fewer it weighs more,
// as the square root of the ratio (HonestyFactsWeight): about the candidates' median when
// it was set (2026-11-11).
const HonestyScoreReferenceFacts = 3000

// HonestyScoreMinFacts is the floor of the facts in the weight: under it the sample is too
// small to tell a rate, and one contradiction among a handful of facts must not be read as
// one in every handful. It caps the weight at sqrt(3000/500), about 2.45: one contradiction
// costs at most 20 points.
const HonestyScoreMinFacts = 500

// ContradictionStats summarises a person's contradictions after stance bundling.
// Bundles is the number of distinct stance-grouped contradictions; Total is the raw pair count.
// Total >= Bundles always; extras := Total - Bundles is the repetition penalty input.
type ContradictionStats struct {
	Bundles int
	Total   int
}

// HonestyFactsWeight is what the facts make of a contradiction's weight:
// sqrt(HonestyScoreReferenceFacts / max(facts, HonestyScoreMinFacts)). The penalty is then
// the geometric mean of two readings. Counting the contradictions alone (weight 1) says
// that enough of them show incoherence whatever was said, but 15 out of 300 facts then read
// as 15 out of 5 000, although anyone who speaks that much ends up contradicting themselves.
// Counting their rate alone (weight 1/facts) forgives everything to who speaks a lot. The
// square root sits between: 4 times the facts halve the weight of each contradiction.
func HonestyFactsWeight(factsCount int) float64 {
	return math.Sqrt(HonestyScoreReferenceFacts / float64(max(factsCount, HonestyScoreMinFacts)))
}

// ComputeHonestyScore returns exp(-w * (j * bundles + alpha * ln(1 + extras))), where
// w = HonestyFactsWeight(facts). Bundling collapses raw pairs that share an underlying
// stance (topic) into one underlying inconsistency, so 10 paraphrase pairs about the same
// broken promise count as one bundle plus a small repetition penalty rather than 10
// independent contradictions.
// If factsCount is 0, returns 1.0 (benefit of the doubt).
func ComputeHonestyScore(factsCount int, stats ContradictionStats) float64 {
	if factsCount == 0 {
		return 1.0
	}
	extras := max(stats.Total-stats.Bundles, 0)
	return math.Exp(-HonestyFactsWeight(factsCount) *
		(HonestyScoreAbsoluteDecay*float64(stats.Bundles) +
			HonestyScoreBundleExtraDecay*math.Log1p(float64(extras))))
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
// their score is already strictly above the lowest score of the covered.
// More facts bring their share of contradictions, whose count grows faster
// than their weight shrinks (HonestyFactsWeight: the square root), so on
// average more facts push such a score further up; a person whose new
// facts hold none goes down instead. A clean sheet needs coverage.
func (b ScoreBaseline) ScoreReliability(facts int, honesty float64) bool {
	if b.CoveredFacts <= 0 {
		return false
	}
	if float64(facts) >= b.CoveredFacts {
		return true
	}
	return b.FloorHonesty != nil && honesty < *b.FloorHonesty
}
