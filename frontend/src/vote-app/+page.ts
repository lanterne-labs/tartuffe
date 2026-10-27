// The root of the vote app: the featured event's vote page. The slug is
// known to the app itself (lib/event.ts); the API, when it answers within
// a moment, may name another featured event. Never a detour by the site:
// the app must work with the site down.
import { redirect } from '@sveltejs/kit';
import { API_BASE } from '$lib/config';
import { FEATURED_SLUG } from '$lib/event';

const API_WAIT = 3000;

export const load = async ({ fetch }: { fetch: typeof globalThis.fetch }) => {
	let slug = FEATURED_SLUG;
	try {
		const res = await fetch(`${API_BASE}/api/polls`, { signal: AbortSignal.timeout(API_WAIT) });
		if (res.ok) {
			const events = (await res.json()) as { slug: string; featured: boolean }[];
			const featured = events.find((e) => e.featured);
			if (featured?.slug) slug = featured.slug;
		}
	} catch (e) {
		console.warn('vote app: the API did not name the featured event, using the built-in one', e);
	}
	redirect(302, `/election/${encodeURIComponent(slug)}/voter`);
};
