// The API as the SvelteKit server reaches it: API_INTERNAL_URL (a private
// network address, at run time) when set, else the public API_BASE of the
// build.
import { env } from '$env/dynamic/private';

export function serverApiBase(): string {
	return (env.API_INTERNAL_URL || import.meta.env.VITE_API_BASE || 'http://localhost:8080').replace(/\/$/, '');
}

// serverFetchJSON fetches a JSON resource of the API, or null when it is
// missing, slow (1.5 s) or failing: a link preview must never break a page.
export async function serverFetchJSON<T>(fetchFn: typeof fetch, path: string): Promise<T | null> {
	try {
		const res = await fetchFn(serverApiBase() + path, { signal: AbortSignal.timeout(1500) });
		if (!res.ok) return null;
		return (await res.json()) as T;
	} catch (err) {
		console.warn(`link preview: ${path}:`, err instanceof Error ? err.message : err);
		return null;
	}
}
