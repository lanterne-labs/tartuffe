// The audience measure: Umami, self-hosted (the host project `analytics`),
// reached through the site itself so the browser never calls another origin:
// /stats/script.js is its tracker, /stats/api/send its collector. No cookie;
// Umami keeps no IP address (it hashes the visitor with a salt that changes
// every day). UMAMI_URL is its origin, UMAMI_WEBSITE_ID this site's id there;
// without both, the site measures nothing.
import { env } from '$env/dynamic/private';

export function umami(): { url: string; website: string } | null {
	const url = env.UMAMI_URL?.replace(/\/+$/, '');
	const website = env.UMAMI_WEBSITE_ID;
	return url && website ? { url, website } : null;
}

// The tag in every page's head: the visitor's "do not track" is honoured, the
// URL's hash is dropped (the Google sign-in's token travels there).
export function umamiTag(): string {
	const u = umami();
	if (!u) return '';
	return `<script defer src="/stats/script.js" data-website-id="${u.website}" data-host-url="/stats" data-do-not-track="true" data-exclude-hash="true"></script>`;
}
