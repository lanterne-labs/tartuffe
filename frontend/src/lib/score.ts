// The honesty score, as backend/core/honesty.go computes it, to explain the
// number shown: the person's inputs come from GET /api/person/{id}/score; a
// stance's from its card (its facts, and its contradiction pairs counted as
// one bundle).

export const RATIO_DECAY = 1.5;
export const ABSOLUTE_DECAY = 0.03;
export const EXTRA_DECAY = 0.05;
export const MIN_DENOMINATOR = 75;

export interface ScoreInputs {
	facts: number;
	bundles: number;
	pairs: number;
}

export interface ScoreTerms {
	denominator: number;
	extras: number;
	ratioTerm: number;
	absoluteTerm: number;
	extraTerm: number;
	score: number;
}

export function scoreTerms({ facts, bundles, pairs }: ScoreInputs): ScoreTerms {
	const denominator = Math.max(facts, MIN_DENOMINATOR);
	const extras = Math.max(0, pairs - bundles);
	const ratioTerm = (RATIO_DECAY * bundles) / denominator;
	const absoluteTerm = ABSOLUTE_DECAY * bundles;
	const extraTerm = EXTRA_DECAY * Math.log1p(extras);
	const score = facts === 0 ? 1 : Math.exp(-ratioTerm - absoluteTerm - extraTerm);
	return { denominator, extras, ratioTerm, absoluteTerm, extraTerm, score };
}

export function stanceInputs(factCount: number, pairs: number): ScoreInputs {
	return { facts: factCount, bundles: pairs > 0 ? 1 : 0, pairs };
}
