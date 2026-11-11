// The honesty score, as backend/core/honesty.go computes it, to explain the
// number shown: the person's inputs come from GET /api/person/{id}/score; a
// stance's from its card (its facts, and its contradiction pairs counted as
// one bundle).

export const ABSOLUTE_DECAY = 0.09;
export const EXTRA_DECAY = 0.125;
/** The facts at which a contradiction has its plain weight. */
export const REFERENCE_FACTS = 3000;
/** The floor of the facts in the weight: under it a sample tells no rate. */
export const MIN_FACTS = 500;

/** A constant of the formula as shown: French writes the decimal with a comma. */
export function formatConstant(n: number, lang: string): string {
	return String(n).replace('.', lang === 'fr' ? ',' : '.');
}

export interface ScoreInputs {
	facts: number;
	bundles: number;
	pairs: number;
}

export interface ScoreTerms {
	denominator: number;
	extras: number;
	/** What the facts make of a contradiction's weight: √(REFERENCE_FACTS / denominator). */
	factsWeight: number;
	absoluteTerm: number;
	extraTerm: number;
	score: number;
}

export function scoreTerms({ facts, bundles, pairs }: ScoreInputs): ScoreTerms {
	const denominator = Math.max(facts, MIN_FACTS);
	const extras = Math.max(0, pairs - bundles);
	const factsWeight = Math.sqrt(REFERENCE_FACTS / denominator);
	const absoluteTerm = ABSOLUTE_DECAY * bundles;
	const extraTerm = EXTRA_DECAY * Math.log1p(extras);
	const score = facts === 0 ? 1 : Math.exp(-factsWeight * (absoluteTerm + extraTerm));
	return { denominator, extras, factsWeight, absoluteTerm, extraTerm, score };
}

export function stanceInputs(factCount: number, pairs: number): ScoreInputs {
	return { facts: factCount, bundles: pairs > 0 ? 1 : 0, pairs };
}
