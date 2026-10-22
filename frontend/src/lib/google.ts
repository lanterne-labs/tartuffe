// Sign-in with Google (OpenID Connect, implicit flow) asking for the scope
// "openid" alone: Google gives the page an ID token that names the account
// by an opaque identifier, with no e-mail, name or photo. A popup runs it so
// the page keeps its state (the vote waiting for the sign-in); the popup
// lands on /auth/google, which hands the token back. When the popup cannot
// reach this page (a browser that cuts the link to it), it signs in itself
// and the session reaches this page through localStorage; without a popup
// at all, the page itself goes to Google and comes back.

import { get } from 'svelte/store';
import { challenge, session, signIn } from './auth';

const PENDING_KEY = 'google_signin';

// Pending is what the callback page needs to finish a sign-in on its own.
export interface Pending {
	state: string;
	nonce: string;
	back: string;
}

export const redirectURI = () => `${window.location.origin}/auth/google`;

function authURL(clientId: string, nonce: string, state: string): string {
	const q = new URLSearchParams({
		client_id: clientId,
		redirect_uri: redirectURI(),
		response_type: 'id_token',
		scope: 'openid',
		nonce,
		state,
		prompt: 'select_account'
	});
	return `https://accounts.google.com/o/oauth2/v2/auth?${q}`;
}

function randomState(): string {
	const b = new Uint8Array(16);
	crypto.getRandomValues(b);
	return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}

export function readPending(): Pending | null {
	try {
		const raw = window.localStorage.getItem(PENDING_KEY);
		return raw ? (JSON.parse(raw) as Pending) : null;
	} catch {
		return null;
	}
}

export function clearPending() {
	try {
		window.localStorage.removeItem(PENDING_KEY);
	} catch {}
}

export class GoogleClosed extends Error {}

// signInWithGoogle signs in with the client of the site. It must run in a
// click handler: the popup opens before anything is awaited.
export async function signInWithGoogle(clientId: string): Promise<void> {
	const popup = window.open('', 'google-signin', 'popup,width=480,height=640');
	try {
		const nonce = await challenge('google');
		const state = randomState();
		const pending: Pending = { state, nonce, back: window.location.pathname + window.location.search };
		try {
			window.localStorage.setItem(PENDING_KEY, JSON.stringify(pending));
		} catch {}
		const url = authURL(clientId, nonce, state);
		if (!popup || popup.closed) {
			window.location.href = url;
			return new Promise(() => {});
		}
		popup.location.href = url;
		const token = await waitForToken(popup, state);
		if (token) await signIn('google', { challenge: nonce, id_token: token });
		clearPending();
	} catch (err) {
		popup?.close();
		throw err;
	}
}

// waitForToken waits for the popup's ID token, or for the session it opened
// on its own (null then), or for it to close.
function waitForToken(popup: Window, state: string): Promise<string | null> {
	return new Promise((resolve, reject) => {
		const before = get(session)?.token;
		const done = () => {
			window.removeEventListener('message', onMessage);
			clearInterval(timer);
			unsub();
		};
		const onMessage = (e: MessageEvent) => {
			if (e.origin !== window.location.origin || e.data?.type !== 'google-signin' || e.data.state !== state) return;
			done();
			if (e.data.id_token) resolve(e.data.id_token);
			else reject(new Error(e.data.error ?? 'Google sign-in failed'));
		};
		const unsub = session.subscribe((s) => {
			if (s && s.token !== before) {
				done();
				resolve(null);
			}
		});
		const timer = setInterval(() => {
			if (popup.closed) {
				done();
				reject(new GoogleClosed('closed'));
			}
		}, 500);
		window.addEventListener('message', onMessage);
	});
}
