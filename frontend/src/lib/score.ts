// The honesty score, as backend/core/honesty.go computes it, to explain the
// number shown: a person's contradictions against those expected of their
// facts at the candidates' average rate. The person's inputs come from
// GET /api/person/{id}/score.

/** The scale: honesty = exp(−DECAY × excess). */
export const DECAY = 0.6;
/** Added to the expected contradictions: with few facts the count decides. */
export const SMOOTHING = 3;
/** What the repeated pairs inside the bundles add: EXTRA_WEIGHT × ln(1 + extras). */
export const EXTRA_WEIGHT = 1.4;
/** The candidates' contradictions per fact on 2026-11-11, until the page has read today's. */
export const DEFAULT_RATE = 0.00078;

/** A constant of the formula as shown: French writes the decimal with a comma. */
export function formatConstant(n: number, lang: string): string {
	return String(n).replace('.', lang === 'fr' ? ',' : '.');
}

/** The candidates' rate from the API's covered_facts (SMOOTHING contradictions expected). */
export function rateFromCovered(coveredFacts: number | null | undefined): number {
	return coveredFacts && coveredFacts > 0 ? SMOOTHING / coveredFacts : DEFAULT_RATE;
}

export interface ScoreInputs {
	facts: number;
	bundles: number;
	pairs: number;
}

export interface ScoreTerms {
	extras: number;
	/** The contradictions as counted: bundles + EXTRA_WEIGHT × ln(1 + extras). */
	weighted: number;
	/** The contradictions expected of the facts: rate × facts. */
	expected: number;
	/** weighted / (expected + SMOOTHING). */
	excess: number;
	score: number;
}

export function scoreTerms({ facts, bundles, pairs }: ScoreInputs, rate: number): ScoreTerms {
	const extras = Math.max(0, pairs - bundles);
	const weighted = bundles + EXTRA_WEIGHT * Math.log1p(extras);
	const expected = rate * facts;
	const excess = weighted / (expected + SMOOTHING);
	const score = facts === 0 ? 1 : Math.exp(-DECAY * excess);
	return { extras, weighted, expected, excess, score };
}
