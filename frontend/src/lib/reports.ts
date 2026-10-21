// The signed-in user's reports on facts and contradictions
// (backend/api/public.go). The worker's judge answers each one within minutes;
// `reported` holds the targets this user already reported, as "kind:id".

import { writable, get } from 'svelte/store';
import { authFetch, session } from './auth';
import type { MyReports, ReportKind, UserReport } from './types';

export type ReportErrorCode = 'already_reported' | 'too_many_reports' | 'not_found' | 'invalid_report' | 'signed_out' | 'error';

export class ReportError extends Error {
	constructor(
		message: string,
		readonly code: ReportErrorCode
	) {
		super(message);
	}
}

export const reported = writable<Set<string>>(new Set());

export const reportKey = (kind: ReportKind, id: number) => `${kind}:${id}`;

// The reported targets load again for each session.
let loadedFor = '';
if (typeof window !== 'undefined') {
	session.subscribe((s) => {
		const token = s?.token ?? '';
		if (token === loadedFor) return;
		loadedFor = token;
		if (!token) {
			reported.set(new Set());
			return;
		}
		getMyReports()
			.then((m) => {
				if (get(session)?.token === token) reported.set(new Set(m.reported));
			})
			.catch((err) => console.error('failed to load my reports', err));
	});
}

async function call<T>(path: string, init: RequestInit = {}): Promise<T> {
	const res = await authFetch(path, init);
	if (res.ok) return res.json();
	let body: { error?: string; code?: ReportErrorCode } = {};
	try {
		body = await res.json();
	} catch {}
	const code = res.status === 401 ? 'signed_out' : (body.code ?? 'error');
	throw new ReportError(body.error ?? `HTTP ${res.status}`, code);
}

export function getMyReports(): Promise<MyReports> {
	return call('/api/me/reports');
}

export async function createReport(kind: ReportKind, targetId: number, reason: string): Promise<UserReport> {
	try {
		const r = await call<UserReport>('/api/reports', {
			method: 'POST',
			body: JSON.stringify({ kind, target_id: targetId, reason })
		});
		reported.update((s) => new Set(s).add(reportKey(kind, targetId)));
		return r;
	} catch (err) {
		if (err instanceof ReportError && err.code === 'already_reported') {
			reported.update((s) => new Set(s).add(reportKey(kind, targetId)));
		}
		throw err;
	}
}

// Bounds of a report's reason, as the API checks them.
export const MIN_REASON = 10;
export const MAX_REASON = 2000;
