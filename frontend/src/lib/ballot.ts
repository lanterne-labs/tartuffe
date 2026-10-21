// The signed-in user's votes (backend/api/polls.go). Every change is saved at
// once; the API answers with the ballot as stored, which the pages show as is.
// A refused change carries a code (ballot_full, consent_required, closed...)
// and, for ballot_full and not_on_ballot, the stored ballot.

import { authFetch } from './auth';
import type { Ballot, MyVotes } from './types';

export type BallotErrorCode = 'ballot_full' | 'not_on_ballot' | 'consent_required' | 'closed' | 'invalid_ballot' | 'signed_out' | 'error';

export class BallotError extends Error {
	constructor(
		message: string,
		readonly code: BallotErrorCode,
		readonly ballot?: Ballot
	) {
		super(message);
	}
}

async function call<T>(path: string, init: RequestInit = {}): Promise<T> {
	const res = await authFetch(path, init);
	if (res.ok) return res.status === 204 ? (undefined as T) : res.json();
	let body: { error?: string; code?: BallotErrorCode; ballot?: Ballot } = {};
	try {
		body = await res.json();
	} catch {}
	const code = res.status === 401 ? 'signed_out' : (body.code ?? 'error');
	throw new BallotError(body.error ?? `HTTP ${res.status}`, code, body.ballot);
}

const pollPath = (slug: string) => `/api/polls/${encodeURIComponent(slug)}`;

export function getBallot(slug: string): Promise<Ballot> {
	return call(`${pollPath(slug)}/ballot`);
}

// addVote votes for personId after the votes already cast, or in place of
// replace (same rank). consent is the user's agreement, needed once.
export async function addVote(slug: string, personId: number, opts: { replace?: number; consent?: boolean } = {}): Promise<Ballot> {
	const b = await call<Ballot>(`${pollPath(slug)}/votes`, {
		method: 'POST',
		body: JSON.stringify({ person_id: personId, replace: opts.replace ?? 0, consent: opts.consent ?? false })
	});
	notifyBallotChanged();
	return b;
}

export async function removeVote(slug: string, personId: number): Promise<Ballot> {
	const b = await call<Ballot>(`${pollPath(slug)}/votes/${personId}`, { method: 'DELETE' });
	notifyBallotChanged();
	return b;
}

// saveBallot replaces the ballot: to reorder the votes, or [] to remove them all.
export async function saveBallot(slug: string, personIds: number[]): Promise<Ballot> {
	const b = await call<Ballot>(`${pollPath(slug)}/ballot`, {
		method: 'POST',
		body: JSON.stringify({ person_ids: personIds })
	});
	notifyBallotChanged();
	return b;
}

export function getMyVotes(): Promise<MyVotes> {
	return call('/api/me/votes');
}

// deleteAccount erases the user, their votes and sessions.
export async function deleteAccount(): Promise<void> {
	await call<void>('/api/auth/me', { method: 'DELETE' });
	notifyBallotChanged();
}

// The other tabs of this browser reload the ballot when one tab changes it.
const channel = typeof BroadcastChannel !== 'undefined' ? new BroadcastChannel('ballot') : null;

function notifyBallotChanged() {
	channel?.postMessage('changed');
}

// onBallotChanged calls back when the ballot may have changed elsewhere: in
// another tab, or on another device (checked when the page is shown again).
export function onBallotChanged(callback: () => void): () => void {
	const onMessage = () => callback();
	const onVisible = () => {
		if (document.visibilityState === 'visible') callback();
	};
	channel?.addEventListener('message', onMessage);
	document.addEventListener('visibilitychange', onVisible);
	return () => {
		channel?.removeEventListener('message', onMessage);
		document.removeEventListener('visibilitychange', onVisible);
	};
}
