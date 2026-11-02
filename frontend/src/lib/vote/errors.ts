// What went wrong, as the voter reads it: every failure of the vote is an
// IsoloirError whose code the page translates (v2.error.<code>; the
// ingest's reasons under v2.reason.* for ballots and v2.reg.* for
// registrations, `reg:<reason>`), with the raw detail apart for the
// curious. No raw English reaches the voter.

import { IsoloirError, toIsoloirError } from '@isoloir/web';
import { tr, type Lang } from '$lib/i18n';

// messageOf reads a message out of anything thrown (the ZKPassport SDK
// throws plain objects).
export function messageOf(e: unknown): string {
	if (e instanceof Error) return e.message;
	if (e && typeof e === 'object' && 'message' in e) return String((e as { message: unknown }).message);
	return String(e);
}

// toVoteError names an error: the library's own, a fetch failure as the
// mirror, the rest under the fallback code with the message as detail.
export function toVoteError(e: unknown, fallback: IsoloirError['code'] = 'unknown'): IsoloirError {
	if (e instanceof IsoloirError) return e;
	const msg = messageOf(e);
	if (e instanceof TypeError || /^HTTP \d+/.test(msg) || /failed to fetch|load failed|networkerror/i.test(msg)) return new IsoloirError('mirror', msg);
	return toIsoloirError(e, fallback);
}

// reportLink is the one way to say "report it": a link to the site's
// corrections page (an absolute address: the vote app lives on another
// origin). The sentences that end with it carry {report}; they are
// rendered as HTML, and they come from the dictionary alone.
export function reportLink(lang: Lang): string {
	return `<a href="${tr('v2.report.url', lang)}" target="_blank" rel="noopener">${tr('v2.report.text', lang)}</a>`;
}

// describeError gives the voter's sentence for a code: what happened and
// what to do, as HTML (the "report it" link). A registration's reason
// reads under v2.reg, a ballot's under v2.reason, the session's under
// v2.error; an unknown code shows the generic sentence. The detail is not
// in the sentence: the page shows it apart, as technical detail.
export function describeError(code: string, lang: Lang): string {
	const vars = { report: reportLink(lang) };
	const keys = code.startsWith('reg:') ? [`v2.reg.${code.slice(4)}`, `v2.reason.${code.slice(4)}`] : [`v2.error.${code}`, `v2.reason.${code}`];
	for (const key of keys) if (tr(key, lang) !== key) return tr(key, lang, vars);
	return tr('v2.error.unknown', lang, vars);
}
