// Signed-in session of the visitor (backend/accounts). The API lives on
// another origin, so the session token travels as a Bearer header (no
// third-party cookie), kept in localStorage so a reload stays signed in.
// An account comes from Google (or noop in tests); voting needs a verified
// identity on top, a document proved with ZKPassport (verify).

import { writable, get } from 'svelte/store';
import { API_BASE } from './config';
import type { AppUser, AuthProvider } from './types';

interface Session {
	token: string;
	user: AppUser;
	expires_at: string;
}

const STORAGE_KEY = 'session';

function readStored(): Session | null {
	if (typeof window === 'undefined') return null;
	try {
		const raw = window.localStorage.getItem(STORAGE_KEY);
		if (!raw) return null;
		const s = JSON.parse(raw) as Session;
		return new Date(s.expires_at) > new Date() ? s : null;
	} catch {
		return null;
	}
}

export const session = writable<Session | null>(readStored());

if (typeof window !== 'undefined') {
	session.subscribe((s) => {
		try {
			if (s) window.localStorage.setItem(STORAGE_KEY, JSON.stringify(s));
			else window.localStorage.removeItem(STORAGE_KEY);
		} catch {}
	});
	// A sign-in or sign-out in another tab (the Google popup when it signs in
	// on its own) reaches this one.
	window.addEventListener('storage', (e) => {
		if (e.key !== STORAGE_KEY) return;
		const next = readStored();
		if (JSON.stringify(next) !== JSON.stringify(get(session))) session.set(next);
	});
}

// Whether the sign-in dialog is open (any page can ask for it).
export const signInOpen = writable(false);

// Whether the identity verification dialog is open (asked before a vote).
export const verifyOpen = writable(false);

let providersLoad: Promise<AuthProvider[]> | null = null;

// loadProviders lists the enabled providers, once per page load.
export function loadProviders(): Promise<AuthProvider[]> {
	if (!providersLoad) {
		providersLoad = fetch(`${API_BASE}/api/auth/providers`).then((res) => {
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			return res.json();
		});
		providersLoad.catch(() => (providersLoad = null));
	}
	return providersLoad;
}

async function errorText(res: Response): Promise<string> {
	try {
		const body = await res.json();
		if (body && typeof body.error === 'string') return body.error;
	} catch {}
	return `HTTP ${res.status}`;
}

// signIn sends an account provider's payload (the ID token for google, a
// pseudonym for noop) and keeps the session the API opens.
export async function signIn(provider: string, payload: unknown): Promise<AppUser> {
	const res = await fetch(`${API_BASE}/api/auth/signin/${encodeURIComponent(provider)}`, {
		method: 'POST',
		headers: { 'Content-Type': 'application/json' },
		body: JSON.stringify(payload)
	});
	if (!res.ok) throw new Error(await errorText(res));
	const s = (await res.json()) as Session;
	session.set(s);
	return s.user;
}

// challenge asks the API for a one-time challenge of the provider (the
// nonce of a Google sign-in, the binding of a passport proof).
export async function challenge(provider: string): Promise<string> {
	const res = await authFetch(`/api/auth/challenge/${encodeURIComponent(provider)}`, { method: 'POST' });
	if (!res.ok) throw new Error(await errorText(res));
	return ((await res.json()) as { challenge: string }).challenge;
}

export class VerifyError extends Error {
	constructor(
		message: string,
		readonly code: string
	) {
		super(message);
	}
}

// verify attaches the document a verification provider proves (the passport
// proof for zkpassport, a pseudonym for noop) to the signed-in account. It
// tells whether the document came from another account (its votes follow).
export async function verify(provider: string, payload: unknown): Promise<{ moved: boolean }> {
	const res = await authFetch(`/api/me/verify/${encodeURIComponent(provider)}`, {
		method: 'POST',
		body: JSON.stringify(payload)
	});
	if (!res.ok) {
		let code = 'error';
		let msg = `HTTP ${res.status}`;
		try {
			const body = await res.json();
			code = body.code ?? code;
			msg = body.error ?? msg;
		} catch {}
		throw new VerifyError(msg, code);
	}
	const out = (await res.json()) as { user: AppUser; moved: boolean };
	setUser(out.user);
	return { moved: out.moved };
}

// setUser replaces the user of the session (after a verification, or a
// refresh that found it changed).
export function setUser(user: AppUser) {
	const s = get(session);
	if (s) session.set({ ...s, user });
}

// refreshUser reads the account again: a document may have moved to another
// account since the session was stored.
export async function refreshUser(): Promise<void> {
	if (!get(session)) return;
	try {
		const res = await authFetch('/api/auth/me');
		if (res.ok) setUser(await res.json());
	} catch (err) {
		console.error('failed to refresh the account', err);
	}
}

export async function signOut(): Promise<void> {
	const s = get(session);
	session.set(null);
	if (!s) return;
	try {
		await fetch(`${API_BASE}/api/auth/signout`, {
			method: 'POST',
			headers: { Authorization: `Bearer ${s.token}` }
		});
	} catch (err) {
		console.error('sign-out request failed', err);
	}
}

// authFetch calls the API with the session; a 401 ends the local session.
export async function authFetch(path: string, init: RequestInit = {}): Promise<Response> {
	const s = get(session);
	const headers = new Headers(init.headers);
	if (s) headers.set('Authorization', `Bearer ${s.token}`);
	if (init.body && !headers.has('Content-Type')) headers.set('Content-Type', 'application/json');
	const res = await fetch(`${API_BASE}${path}`, { ...init, headers });
	if (res.status === 401) session.set(null);
	return res;
}

export { errorText };
