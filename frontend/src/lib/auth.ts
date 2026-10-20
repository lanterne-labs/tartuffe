// Signed-in session of the visitor. The API lives on another origin, so the
// session token travels as a Bearer header (no third-party cookie), kept in
// localStorage so a reload stays signed in.

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
}

// Whether the sign-in dialog is open (any page can ask for it).
export const signInOpen = writable(false);

export async function loadProviders(): Promise<AuthProvider[]> {
	const res = await fetch(`${API_BASE}/api/auth/providers`);
	if (!res.ok) throw new Error(`HTTP ${res.status}`);
	return res.json();
}

async function errorText(res: Response): Promise<string> {
	try {
		const body = await res.json();
		if (body && typeof body.error === 'string') return body.error;
	} catch {}
	return `HTTP ${res.status}`;
}

// signIn sends the provider's payload (a pseudonym for noop, the proof for
// zkpassport) and keeps the session the API opens.
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
