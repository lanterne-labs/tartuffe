// A service worker for the vote pages: it adds the cross-origin isolation
// headers to every response of this origin, so the prover gets
// SharedArrayBuffer and threads (static hosts such as GitHub Pages cannot
// send them). The prover's large files (the SRS, bb.js's wasm) come from
// jsDelivr, pinned by hash in src/lib/vote/isoloir.ts.
// Registered by the vote page; the first visit reloads once.

self.addEventListener('install', () => self.skipWaiting());
self.addEventListener('activate', (e) => e.waitUntil(self.clients.claim()));

self.addEventListener('fetch', (e) => {
	const req = e.request;
	const url = new URL(req.url);
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
