// Umami's tracker, served from the site (lib/server/umami.ts).
import { error } from '@sveltejs/kit';
import { umami } from '$lib/server/umami';
import type { RequestHandler } from './$types';

export const GET: RequestHandler = async ({ fetch }) => {
	const u = umami();
	if (!u) error(404);
	const res = await fetch(`${u.url}/script.js`);
	if (!res.ok) {
		console.error('umami: script', res.status);
		error(502);
	}
	return new Response(await res.text(), {
		headers: { 'content-type': 'application/javascript; charset=utf-8', 'cache-control': 'public, max-age=3600' }
	});
};
