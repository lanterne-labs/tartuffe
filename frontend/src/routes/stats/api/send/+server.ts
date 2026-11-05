// Umami's collector, reached through the site (lib/server/umami.ts). Umami
// needs the visitor's address for the country and the day's hash, then drops
// it: it travels in the header Umami reads (its CLIENT_IP_HEADER), from
// the host's edge (X-Real-IP).
import { error } from '@sveltejs/kit';
import { umami } from '$lib/server/umami';
import type { RequestHandler } from './$types';

export const POST: RequestHandler = async ({ request, fetch, getClientAddress }) => {
	const u = umami();
	if (!u) error(404);
	const headers: Record<string, string> = {
		'content-type': 'application/json',
		'user-agent': request.headers.get('user-agent') ?? '',
		'x-umami-client-ip': request.headers.get('x-real-ip') ?? getClientAddress()
	};
	const lang = request.headers.get('accept-language');
	if (lang) headers['accept-language'] = lang;
	try {
		const res = await fetch(`${u.url}/api/send`, { method: 'POST', headers, body: await request.text() });
		return new Response(await res.text(), { status: res.status, headers: { 'content-type': res.headers.get('content-type') ?? 'application/json' } });
	} catch (e) {
		console.error('umami: send', e);
		error(502);
	}
};
