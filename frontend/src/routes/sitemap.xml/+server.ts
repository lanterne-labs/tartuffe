// The sitemap: the site's pages, the persons above the public floor, the
// events. Read by crawlers; built from the API at each request.
import { env } from '$env/dynamic/private';
import { API_BASE } from '$lib/config';
import type { RequestHandler } from './$types';

const STATIC = ['/', '/dashboard', '/feed', '/about', '/about/privacy', '/corrections', '/lab'];

export const GET: RequestHandler = async ({ url, fetch }) => {
	const origin = url.origin;
	const api = env.API_INTERNAL_URL || API_BASE;
	const urls: string[] = STATIC.map((p) => origin + p);
	try {
		const events = (await (await fetch(`${api}/api/polls`)).json()) as { slug: string }[];
		for (const e of events) {
			const base = `${origin}/election/${encodeURIComponent(e.slug)}`;
			urls.push(base, `${base}/voter`, `${base}/fonctionnement`);
		}
	} catch (e) {
		console.error('sitemap: events', e);
	}
	try {
		const persons = (await (await fetch(`${api}/api/persons?limit=1000`)).json()) as { id: number }[] | { persons?: { id: number }[] };
		const list = Array.isArray(persons) ? persons : (persons.persons ?? []);
		for (const p of list) urls.push(`${origin}/person/${p.id}`);
	} catch (e) {
		console.error('sitemap: persons', e);
	}
	const body = `<?xml version="1.0" encoding="UTF-8"?>\n<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">\n${urls.map((u) => `  <url><loc>${u.replace(/&/g, '&amp;')}</loc></url>`).join('\n')}\n</urlset>\n`;
	return new Response(body, { headers: { 'content-type': 'application/xml; charset=utf-8', 'cache-control': 'public, max-age=3600' } });
};
