// Links to share a person, a stance, a contradiction or a fact. Every item
// opens on its person's page: ?stance=, ?c= (contradiction) or ?fact= say
// which stance to open and what to highlight; the server reads the same
// parameters to fill the link preview (routes/person/[id]/+page.server.ts).

import type { ShareKind } from './types';
import { tr, type Lang } from './i18n';

export interface ShareItem {
	kind: ShareKind;
	id: number;
	personId: number;
	personName: string;
	// The person's score, for a person; the topic of a stance, contradiction or fact.
	score?: number;
	topic?: string;
	text?: string;
	other?: string;
	// The event's slug, for an event.
	slug?: string;
}

// displayName capitalizes a name stored in lowercase ("jean-luc mélenchon"
// -> "Jean-Luc Mélenchon"), as the pages do with CSS, for the texts that
// leave the site (posts, link previews). A name with capitals is kept.
export function displayName(name: string): string {
	if (name !== name.toLowerCase()) return name;
	return name.replace(/(^|[\s\-'’])(\p{L})/gu, (_, sep: string, c: string) => sep + c.toUpperCase());
}

export function sharePath(kind: ShareKind, id: number, personId: number, slug = ''): string {
	switch (kind) {
		case 'event':
			return `/election/${encodeURIComponent(slug)}`;
		case 'person':
			return `/person/${personId}`;
		case 'stance':
			return `/person/${personId}?stance=${id}`;
		case 'contradiction':
			return `/person/${personId}?c=${id}`;
		case 'fact':
			return `/person/${personId}?fact=${id}`;
	}
}

export function shareURL(item: ShareItem): string {
	const origin = typeof window === 'undefined' ? '' : window.location.origin;
	return origin + sharePath(item.kind, item.id, item.personId, item.slug);
}

export function clip(s: string, n: number): string {
	s = s.replace(/\s+/g, ' ').trim();
	return s.length > n ? s.slice(0, n - 1).trimEnd() + '…' : s;
}

// shareText is the text of a post about the item, short enough for X with
// its link (a link counts 23 of the 280 characters).
export function shareText(item: ShareItem, l: Lang): string {
	const name = displayName(item.personName);
	switch (item.kind) {
		case 'event':
			return tr('share.text.event', l, { name });
		case 'person':
			return tr('share.text.person', l, { name, score: Math.round((item.score ?? 0) * 100) });
		case 'stance':
			return tr('share.text.stance', l, { name, topic: clip(item.topic ?? '', 60) });
		case 'contradiction':
			return tr('share.text.contradiction', l, { name, a: clip(item.text ?? '', 90), b: clip(item.other ?? '', 90) });
		case 'fact':
			return tr('share.text.fact', l, { name, fact: clip(item.text ?? '', 180) });
	}
}

export function xIntentURL(item: ShareItem, l: Lang): string {
	const q = new URLSearchParams({ text: shareText(item, l), url: shareURL(item) });
	return `https://twitter.com/intent/tweet?${q}`;
}

export async function copyText(text: string): Promise<boolean> {
	try {
		await navigator.clipboard.writeText(text);
		return true;
	} catch {
		// Older browsers and non-secure origins: a hidden textarea.
		const ta = document.createElement('textarea');
		ta.value = text;
		ta.setAttribute('readonly', '');
		ta.style.position = 'fixed';
		ta.style.opacity = '0';
		document.body.appendChild(ta);
		ta.select();
		const ok = document.execCommand('copy');
		ta.remove();
		return ok;
	}
}
