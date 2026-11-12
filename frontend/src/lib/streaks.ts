// The days without a contradiction of each candidate: how long ago their
// latest contradiction was (`last_contradiction_at` of GET /api/persons:
// the latest dated fact among their contradictions). The longest first. A
// candidate without a contradiction has no streak: nothing started it.
import { API_BASE } from '$lib/config';
import { statementDateText } from '$lib/dates';
import type { PersonSummary } from '$lib/types';

export interface Streak {
	person: PersonSummary;
	// Whole days since the last contradiction.
	days: number;
	// When only the month or the year of the statement is known, a count of
	// days would be made up: the date itself is shown instead.
	since: string;
}

export async function loadCandidates(): Promise<PersonSummary[]> {
	const res = await fetch(`${API_BASE}/api/persons?candidate=true&limit=100`);
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	return ((await res.json()) as PersonSummary[]) ?? [];
}

export function streaksOf(candidates: PersonSummary[], lang: string): Streak[] {
	const out: Streak[] = [];
	for (const p of candidates) {
		if (!p.last_contradiction_at) continue;
		const precision = p.last_contradiction_precision;
		out.push({
			person: p,
			days: Math.max(0, Math.floor((Date.now() - new Date(p.last_contradiction_at).getTime()) / 86_400_000)),
			since: precision === 'year' || precision === 'month' ? statementDateText(p.last_contradiction_at, precision, lang) : ''
		});
	}
	return out.sort((a, b) => b.days - a.days);
}
