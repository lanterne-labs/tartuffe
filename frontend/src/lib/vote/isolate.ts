// Cross-origin isolation for the prover (SPEC §6.5): registers the
// service worker that adds the headers, and reloads once so the page runs
// under it. Returns whether the page is isolated now.
export async function ensureIsolated(): Promise<boolean> {
	if (typeof window === 'undefined') return false;
	if (window.crossOriginIsolated) return true;
	if (!('serviceWorker' in navigator)) return false;
	try {
		const reg = await navigator.serviceWorker.register('/coi-sw.js', { scope: '/' });
		await reg.update().catch(() => {});
		if (!navigator.serviceWorker.controller) {
			// First visit: the worker controls the page after a reload.
			const reloaded = sessionStorage.getItem('coi-reloaded');
			if (!reloaded) {
				sessionStorage.setItem('coi-reloaded', '1');
				window.location.reload();
			}
		}
	} catch (e) {
		console.warn('service worker', e);
	}
	return window.crossOriginIsolated;
}
