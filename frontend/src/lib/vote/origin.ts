// The vote app's origin (SPEC §6.5): the ZKPassport domain and the
// passkey RP are bound to one origin for the life of the event, and the
// keys live in that origin's storage. When the site hosts a copy of the
// vote routes, a visit there goes to the app: VITE_VOTE_APP_ORIGIN names
// it (unset in dev and in the app's own build).

import { browser } from '$app/environment';
import { redirect } from '@sveltejs/kit';

export const VOTE_APP_ORIGIN = ((import.meta.env.VITE_VOTE_APP_ORIGIN as string | undefined) ?? '').replace(/\/$/, '');

// redirectToVoteApp sends a page of the vote to the app's origin, same
// path, when this is not it. In the browser the fragment goes along (a
// device link lives there); a server redirect keeps it by the browser's
// own rule.
export function redirectToVoteApp(url: URL): void {
	if (!VOTE_APP_ORIGIN) return;
	const here = browser ? window.location.origin : url.origin;
	if (here === VOTE_APP_ORIGIN) return;
	const hash = browser ? window.location.hash : '';
	redirect(302, `${VOTE_APP_ORIGIN}${url.pathname}${url.search}${hash}`);
}
