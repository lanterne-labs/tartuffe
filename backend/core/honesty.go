package core

import "math"

// HonestyScoreRatioDecay controls the exponential penalty based on the bundle/facts ratio.
// Kept moderate (was 4) so a single contradiction in a tiny fact base does not crush the score.
const HonestyScoreRatioDecay = 1.5

// HonestyScoreAbsoluteDecay controls the exponential penalty per distinct contradiction bundle.
// Each bundle contributes one full penalty regardless of how many raw pairs it contains.
// Sized so that one contradiction yields ~95% honesty and the score decays exponentially
// from there as more contradictions are added.
const HonestyScoreAbsoluteDecay = 0.03

// HonestyScoreBundleExtraDecay controls the (logarithmic) penalty for repeated contradictions
// inside the same bundle. extras = total raw pairs - distinct bundles. The constant 0.05 is
// chosen so that any bundle of size N >= 2 strictly reduces the penalty vs. counting each pair
// independently.
const HonestyScoreBundleExtraDecay = 0.05

// HonestyScoreMinDenominator is the Bayesian-style smoothing floor for the ratio term:
// ratio = bundles / max(facts, HonestyScoreMinDenominator). Without it, a person with
// 1 contradiction out of 2 facts gets crushed even though the sample size is too small
// to justify that confidence. The floor of 75 keeps a single contradiction near ~95%
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
// must be expected to hold, at the candidates' average rate, before the
// score is trusted: below it, finding none by chance is likelier than 5 %
// (Poisson: e^-3), so a clean score says little.
const ScoreReliableExpected = 3.0

// ScoreReliability returns the contradictions expected in facts at rate
// (contradictions per fact) and whether the score built on them is
// reliable. With no rate yet (no contradiction found anywhere), no score is.
func ScoreReliability(facts int, rate float64) (expected float64, reliable bool) {
	expected = float64(facts) * rate
	return expected, rate > 0 && expected >= ScoreReliableExpected
}
