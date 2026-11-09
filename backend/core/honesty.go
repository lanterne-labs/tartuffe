package core

import "math"

// HonestyScoreRatioDecay controls the exponential penalty based on the bundle/facts ratio.
// It only matters near the smoothing floor: with the thousands of facts a tracked person
// has, 1.5 * B/F is a few thousandths and the two terms below decide the score.
const HonestyScoreRatioDecay = 1.5

// HonestyScoreAbsoluteDecay controls the exponential penalty per distinct contradiction bundle.
// Each bundle contributes one full penalty regardless of how many raw pairs it contains.
// Sized (2026-11-09, from 0.03) so that one contradiction yields ~91% honesty (a
// Tartuffe-Score of 9) and seven distinct contradictions about 50: the candidates hold
// 1 to 7 bundles, and at 0.03 the most contradicted of them read 33 with 45 pairs.
const HonestyScoreAbsoluteDecay = 0.09

// HonestyScoreBundleExtraDecay controls the (logarithmic) penalty for repeated contradictions
// inside the same bundle. extras = total raw pairs - distinct bundles. It must stay under
// HonestyScoreAbsoluteDecay / ln 2 (0.1298) so that a bundle of N >= 2 pairs always costs
// strictly less than N distinct bundles: j + a*ln(N) < j*N, binding at N = 2. At 0.125, 45
// pairs in 7 bundles cost 0.46 on top of the bundles' 0.63 (a Tartuffe-Score of 66 against
// 59 for 14 pairs in 7 bundles); at 0.05 they cost 0.18 and the two read 6 points apart.
const HonestyScoreBundleExtraDecay = 0.125

// HonestyScoreMinDenominator is the Bayesian-style smoothing floor for the ratio term:
// ratio = bundles / max(facts, HonestyScoreMinDenominator). Without it, a person with
// 1 contradiction out of 2 facts gets crushed even though the sample size is too small
// to justify that confidence. The floor of 75 keeps a single contradiction near ~90%
// honesty until enough facts accumulate to confirm the ratio is real.
const HonestyScoreMinDenominator = 75

// ContradictionStats summarises a person's contradictions after stance bundling.
// Bundles is the number of distinct stance-grouped contradictions; Total is the raw pair count.
// Total >= Bundles always; extras := Total - Bundles is the repetition penalty input.
type ContradictionStats struct {
	Bundles int
	Total   int
}

// ComputeHonestyScore returns exp(-k * bundles/denom - j * bundles - alpha * ln(1 + extras)),
// where denom = max(facts, HonestyScoreMinDenominator). Bundling collapses raw pairs that
// share an underlying stance (topic) into one underlying inconsistency, so 10 paraphrase
// pairs about the same broken promise count as one bundle plus a small repetition penalty
// rather than 10 independent contradictions. The smoothing floor prevents one contradiction
// at very low fact counts from collapsing the score.
// If factsCount is 0, returns 1.0 (benefit of the doubt).
func ComputeHonestyScore(factsCount int, stats ContradictionStats) float64 {
	if factsCount == 0 {
		return 1.0
	}
	extras := stats.Total - stats.Bundles
	if extras < 0 {
		extras = 0
	}
	denom := factsCount
	if denom < HonestyScoreMinDenominator {
		denom = HonestyScoreMinDenominator
	}
	ratio := float64(stats.Bundles) / float64(denom)
	return math.Exp(
		-HonestyScoreRatioDecay*ratio -
			HonestyScoreAbsoluteDecay*float64(stats.Bundles) -
			HonestyScoreBundleExtraDecay*math.Log1p(float64(extras)))
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
// score can only be too low, never too high: a person is reliable when
// covered (enough facts for a clean score to mean something), or when
// their score is already strictly above the lowest score of the covered,
// which more facts can only push further up. A clean sheet needs coverage.
func (b ScoreBaseline) ScoreReliability(facts int, honesty float64) bool {
	if b.CoveredFacts <= 0 {
		return false
	}
	if float64(facts) >= b.CoveredFacts {
		return true
	}
	return b.FloorHonesty != nil && honesty < *b.FloorHonesty
}
