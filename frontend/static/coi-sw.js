// A service worker for the vote pages (SPEC §6.5):
//  - it adds the cross-origin isolation headers to every response of this
//    origin, so the prover gets SharedArrayBuffer and threads (static hosts
//    such as GitHub Pages cannot send them);
//  - it serves the SRS that bb.js asks Aztec's CDN for from our own host
//    (/srs/…), so no third party sees who prepares a ballot.
// Registered by the vote page; the first visit reloads once.
const SRS_HOSTS = ['https://crs.aztec-cdn.foundation', 'https://crs.aztec-labs.com'];

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', (e) => {
	const req = e.request;
	const url = new URL(req.url);
	for (const host of SRS_HOSTS) {
		if (req.url.startsWith(host)) {
			const local = new URL('/srs' + url.pathname, self.location.origin);
			// The Range header of the request is kept: the file is large and
			// bb.js asks for the points it needs.
			e.respondWith(fetch(local.href, { headers: req.headers }).then(withIsolation));
			return;
		}
	}
	if (url.origin !== self.location.origin || req.cache === 'only-if-cached' && req.mode !== 'same-origin') return;
	e.respondWith(fetch(req).then(withIsolation).catch(() => fetch(req)));
});

function withIsolation(res) {
	if (res.status === 0 || res.type === 'opaque') return res;
	const headers = new Headers(res.headers);
	headers.set('Cross-Origin-Embedder-Policy', 'credentialless');
	headers.set('Cross-Origin-Opener-Policy', 'same-origin');
	headers.set('Cross-Origin-Resource-Policy', 'cross-origin');
	return new Response(res.body, { status: res.status, statusText: res.statusText, headers });
}
