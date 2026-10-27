// The root of the vote app: the featured event's vote page.
import { redirect } from '@sveltejs/kit';
import { API_BASE } from '$lib/config';

export const load = async ({ fetch }: { fetch: typeof globalThis.fetch }) => {
	try {
		const events = (await (await fetch(`${API_BASE}/api/polls`)).json()) as { slug: string; featured: boolean }[];
		const featured = events.find((e) => e.featured) ?? events[0];
		if (featured) redirect(302, `/election/${encodeURIComponent(featured.slug)}/voter`);
	} catch (e) {
		if (e && typeof e === 'object' && 'status' in e) throw e;
	}
	redirect(302, 'https://tartuffe.me');
};
