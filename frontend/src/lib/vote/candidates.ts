// What the site knows of an event's candidates: their person id, photo
// and honesty score, from GET /api/polls/<slug>. The poll's setup names
// its options in slot order (blank last); this maps each name to the
// site's card. The published vote app never calls the API: its build
// fetches the same data into static/vote/candidates/<slug>.json
// (scripts/vote-app.mjs), and VITE_VOTE_STATIC_CANDIDATES=1 reads that.

import { base } from '$app/paths';
import { API_BASE } from '$lib/config';
import type { PollEvent, PollResponse } from '$lib/types';

export interface Candidate {
	name: string;
	person_id?: number;
	image_url?: string;
	honesty_score?: number;
}

export interface EventMeta {
	event: PollEvent | null;
	candidates: Candidate[];
}

// metaFromPoll keeps what the vote pages show of an API answer.
export function metaFromPoll(p: PollResponse): EventMeta {
	return {
		event: p.event,
		candidates: p.results.options
			.filter((o) => o.kind === 'candidate')
			.map((o) => ({ name: o.name ?? '', person_id: o.person_id ?? undefined, image_url: o.image_url ?? undefined, honesty_score: o.honesty_score ?? undefined }))
	};
}

export async function loadEventMeta(slug: string): Promise<EventMeta> {
	const fromStatic = import.meta.env.VITE_VOTE_STATIC_CANDIDATES === '1';
	const url = fromStatic ? `${base}/vote/candidates/${encodeURIComponent(slug)}.json` : `${API_BASE}/api/polls/${encodeURIComponent(slug)}`;
	const res = await fetch(url);
	if (!res.ok) throw new Error(`${url}: HTTP ${res.status}`);
	const body = await res.json();
	return fromStatic ? (body as EventMeta) : metaFromPoll(body as PollResponse);
}

// The key a name is matched by: the same letters whatever the case, the
// spaces and the Unicode form.
export function nameKey(name: string): string {
	return name.normalize('NFC').trim().replace(/\s+/g, ' ').toLocaleLowerCase('fr');
}

export function byName(list: Candidate[]): Map<string, Candidate> {
	return new Map(list.map((c) => [nameKey(c.name), c]));
}
