// The data of the design mock-ups (/lab/design): the podium, every candidate
// with a score, and the hottest contradictions of the feed, in the shapes the
// mock-ups draw. Read from the API as the site reads them.
import { API_BASE } from '$lib/config';
import type { PersonSummary } from '$lib/types';

export interface Laureate {
	id: number;
	name: string;
	/** The Tartuffe-Score, 0 (coherent) to 100 (tartuffe). */
	score: number;
	facts: number;
	contradictions: number;
	reliable: boolean;
	image?: string;
}

export interface Side {
	text: string;
	quote: string;
	date: string;
	year: string;
	source: string;
	domain: string;
}

export interface Clash {
	id: number;
	person: Laureate;
	topic: string;
	a: Side;
	b: Side;
	reason: string;
	/** The points the Tartuffe-Score rose by. */
	rise: number;
}

export interface Landing {
	podium: Laureate[];
	candidates: Laureate[];
	clashes: Clash[];
}

export function titleCase(name: string): string {
	return name.replace(/(^|[\s-])(\p{L})/gu, (m) => m.toUpperCase());
}

function laureate(p: PersonSummary): Laureate {
	return {
		id: p.id,
		name: titleCase(p.name),
		score: Math.round((1 - (p.honesty_score ?? 1)) * 100),
		facts: p.fact_count ?? 0,
		contradictions: p.contradiction_count ?? 0,
		reliable: p.score_reliable !== false,
		image: p.image_url ?? undefined
	};
}

function side(f: { text: string; quote?: string; date?: string; source_urls?: string[] }): Side {
	const date = f.date ? new Date(f.date) : null;
	const source = f.source_urls?.[0] ?? '';
	let domain = '';
	try {
		domain = source ? new URL(source).hostname.replace(/^www\./, '') : '';
	} catch {
		domain = '';
	}
	return {
		text: f.text,
		quote: f.quote ?? '',
		date: date ? date.toLocaleDateString('fr-FR', { day: 'numeric', month: 'long', year: 'numeric' }) : '',
		year: date ? String(date.getFullYear()) : '',
		source,
		domain
	};
}

export async function loadPodium(): Promise<Laureate[]> {
	return (await loadLanding()).podium;
}

export async function loadLanding(): Promise<Landing> {
	const qs = new URLSearchParams({ limit: '100', sort: 'honesty_asc', min_facts: '3', candidate: 'true' });
	const [peopleRes, feedRes] = await Promise.all([
		fetch(`${API_BASE}/api/persons?${qs}`),
		fetch(`${API_BASE}/api/feed?sort=hot&kind=contradiction&limit=8`)
	]);
	if (!peopleRes.ok) throw new Error(`HTTP ${peopleRes.status}`);
	const candidates = (((await peopleRes.json()) as PersonSummary[] | null) ?? []).map(laureate);
	const podium = candidates.filter((p) => p.reliable).slice(0, 3);
	let clashes: Clash[] = [];
	if (feedRes.ok) {
		const feed = (await feedRes.json()) as { items?: Record<string, never>[] };
		const byId = new Map(candidates.map((c) => [c.id, c]));
		clashes = (feed.items ?? []).map((it) => {
			const i = it as unknown as {
				id: number;
				person: PersonSummary;
				stance_label: string;
				fact: Parameters<typeof side>[0];
				other: Parameters<typeof side>[0];
				reason: string;
				score_effect?: number;
			};
			// The older fact first: the contradiction reads as a story.
			const [x, y] = [side(i.fact), side(i.other)];
			const [a, b] = (i.fact.date ?? '') <= (i.other.date ?? '') ? [x, y] : [y, x];
			return {
				id: i.id,
				// The feed's card has no contradiction count: the candidate's row does.
				person: byId.get(i.person.id) ?? laureate(i.person),
				topic: i.stance_label,
				a,
				b,
				reason: i.reason,
				// score_effect is the move of the Tartuffe-Score, in points, negative when it rises.
				rise: Math.max(1, Math.round(-(i.score_effect ?? 0)))
			};
		});
	}
	return { podium, candidates, clashes };
}
