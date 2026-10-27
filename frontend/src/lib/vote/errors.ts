// What went wrong, named: every failure the vote page can meet carries a
// code the UI translates (v2.error.<code>, the acknowledgement reasons
// under v2.reason.* for ballots and v2.reg.* for registrations) and the
// raw detail for the curious. No raw English reaches the voter.

import { tr, type Lang } from '$lib/i18n';

export class VoteError extends Error {
	constructor(
		readonly code: string,
		readonly detail = ''
	) {
		super(detail ? `${code}: ${detail}` : code);
		this.name = 'VoteError';
	}
}

// messageOf reads a message out of anything thrown (the ZKPassport SDK
// throws plain objects).
export function messageOf(e: unknown): string {
	if (e instanceof Error) return e.message;
	if (e && typeof e === 'object' && 'message' in e) return String((e as { message: unknown }).message);
	return String(e);
}

// toVoteError names an error: a fetch failure is the mirror, the rest keeps
// the fallback code with the message as detail.
export function toVoteError(e: unknown, fallback = 'unknown'): VoteError {
	if (e instanceof VoteError) return e;
	const msg = messageOf(e);
	if (e instanceof TypeError || /^HTTP \d+/.test(msg) || /failed to fetch|load failed|networkerror/i.test(msg)) return new VoteError('mirror', msg);
	return new VoteError(fallback, msg);
}

// describeError gives the voter's sentence for a code: what happened and
// what to do. A ballot reason (stale_seq…) reads under v2.reason, a
// registration reason under v2.reg, the rest under v2.error; an unknown
// code shows the generic sentence with the detail.
export function describeError(code: string, detail: string, lang: Lang): string {
	for (const key of [`v2.error.${code}`, `v2.reason.${code}`, code.startsWith('reg:') ? `v2.reg.${code.slice(4)}` : '']) {
		if (key && tr(key, lang) !== key) return tr(key, lang);
	}
	return tr('v2.error.unknown', lang, { detail: detail || code });
}
