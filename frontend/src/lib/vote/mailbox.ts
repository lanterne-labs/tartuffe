// The mailbox (SPEC §7): where the browser posts registrations and ballots
// and reads the server's acknowledgements and checkpoints. A set of named
// boxes anyone can read; today Nostr relays, one box per relay URL. The
// vote code depends on the Mailbox interface only: another transport is
// one more implementation here, with its twin in backend/vote/mailbox.go.

import { schnorr } from '@noble/curves/secp256k1.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils.js';
import { bech32 } from '@scure/base';

export type MessageKind = 'ballot' | 'register' | 'complaint' | 'link';
export type ServerKind = 'ack' | 'checkpoint' | 'snapshot';

// Posted is the receipt of a post: the message's id in the box.
export interface Posted {
	id: string;
	box: string;
}

// Prepared is a message signed and ready to send, the same to every box:
// its id is known before any box took it, so the device can keep it
// (a registration survives a closed tab) and send it again as it is.
export interface Prepared {
	id: string;
	// raw is the transport's form of the message, serializable.
	raw: unknown;
}

// ServerMessage is one the server posted to a box.
export interface ServerMessage {
	kind: ServerKind;
	body: string;
	time: number;
	// event: the signed event itself (a complaint carries the ack's).
	event: NostrEvent;
	// live: delivered after the subscription caught up with the box's
	// stored events, so posted moments ago (a checkpoint's time is then
	// the server's now).
	live: boolean;
}

// Registration is a registration as read from a box (the newest leaves).
export interface RegisterMessage {
	id: string;
	body: string;
	time: number;
}

export interface Mailbox {
	boxes(): string[];
	// prepare signs a voter message; send posts it to one box and resolves
	// once the box took it, or rejects. post does both. tags are added to
	// the event's (a link names its receiver with a t tag).
	prepare(kind: MessageKind, body: string, tags?: string[][]): Prepared;
	send(box: string, msg: Prepared): Promise<Posted>;
	post(box: string, kind: MessageKind, body: string, tags?: string[][]): Promise<Posted>;
	// serverMessages reads the server's messages of a kind posted to the
	// box since the time given (0: whatever the box serves), live, until
	// the subscription is stopped; only messages the server signed are
	// given. down is told once when the box cannot be reached three times
	// in a row or refuses the subscription (the caller moves to another
	// box).
	serverMessages(box: string, kind: ServerKind, since: number, each: (m: ServerMessage) => void, down?: () => void): () => void;
	// registrations reads the voters' registrations posted to the box
	// since the time given, live, until stopped (the newest leaves, before
	// the mirror has them).
	registrations(box: string, since: number, each: (m: RegisterMessage) => void): () => void;
	// fetchEvent reads one event by its id from whichever box holds it
	// (a complaint someone links to); null when none does in time.
	fetchEvent(id: string, timeoutMs: number): Promise<NostrEvent | null>;
	// links reads the device links posted to the box for a receiver key
	// (SPEC §4.2), live, until stopped.
	links(box: string, receiver: string, since: number, each: (sealed: string) => void): () => void;
}

// --- The server's clock ---
//
// A device's clock may be off by minutes: its events would be refused or
// its acknowledgement subscription would start too late. Every signed
// checkpoint carries the server's time. A checkpoint reaches us after it
// was signed, never before, so (its time − local time) is a lower bound
// on the offset: it corrects a clock that is behind, never one that is
// ahead (an old checkpoint says nothing about that). A checkpoint that
// arrives live (after the subscription caught up) was signed seconds ago:
// its offset is the truth, either way.

let bound: number | null = null;
let live: number | null = null;
let lastCheckpointTime = 0;

export function noteServerTime(unixSeconds: number, isLive = false) {
	const o = unixSeconds * 1000 - Date.now();
	bound = bound === null ? o : Math.max(bound, o);
	if (isLive) live = o;
	lastCheckpointTime = Math.max(lastCheckpointTime, unixSeconds);
}

export const clockKnown = () => bound !== null;

// serverNow is the time as the server counts it, in milliseconds.
export const serverNow = () => Date.now() + (live ?? Math.max(0, bound ?? 0));

// sinceRecent gives a subscription's starting time: a window before the
// newest checkpoint's time, which is at or before the server's now
// whatever the local clock says; before any checkpoint, before now as
// best known.
export function sinceRecent(windowSeconds: number): number {
	const base = lastCheckpointTime || Math.floor(serverNow() / 1000);
	return Math.max(0, base - windowSeconds);
}

// --- Nostr ---

// The kinds of backend/vote/nostr.go.
const KINDS: Record<MessageKind | ServerKind, number> = { ballot: 7020, register: 7021, ack: 7022, checkpoint: 7023, complaint: 7024, snapshot: 7025, link: 7026 };

export interface NostrEvent {
	id: string;
	pubkey: string;
	created_at: number;
	kind: number;
	tags: string[][];
	content: string;
	sig: string;
}

// signedEvent signs the content as an event of a fresh throwaway key,
// dated to the minute on the server's clock.
function signedEvent(kind: number, tag: string, content: string, extraTags: string[][] = [], now = serverNow()): NostrEvent {
	const secret = schnorr.utils.randomSecretKey();
	const pubkey = bytesToHex(schnorr.getPublicKey(secret));
	const created_at = Math.floor(now / 60_000) * 60;
	const tags = [['t', tag], ...extraTags];
	const serial = JSON.stringify([0, pubkey, created_at, kind, tags, content]);
	const id = sha256(new TextEncoder().encode(serial));
	return { id: bytesToHex(id), pubkey, created_at, kind, tags, content, sig: bytesToHex(schnorr.sign(id, secret)) };
}

export const linkTag = (receiver: string) => `link:${receiver}`;

// eventId recomputes a Nostr event's id; verifyEvent checks it and the
// signature (what a complaint's reader does with the ack inside).
export function eventId(ev: NostrEvent): string {
	const serial = JSON.stringify([0, ev.pubkey, ev.created_at, ev.kind, ev.tags, ev.content]);
	return bytesToHex(sha256(new TextEncoder().encode(serial)));
}

export function verifyEvent(ev: NostrEvent): boolean {
	try {
		return eventId(ev) === ev.id && schnorr.verify(hexToBytes(ev.sig), hexToBytes(ev.id), hexToBytes(ev.pubkey));
	} catch {
		return false;
	}
}

// neventLink is a public page of the event (njump renders any Nostr
// event): an nevent code with the relays as hints.
export function neventLink(id: string, relays: string[]): string {
	const tlv: number[] = [0, 32, ...hexToBytes(id)];
	for (const r of relays.slice(0, 3)) {
		const b = new TextEncoder().encode(r);
		tlv.push(1, b.length, ...b);
	}
	return 'https://njump.me/' + bech32.encode('nevent', bech32.toWords(Uint8Array.from(tlv)), 5000);
}

const POST_TIMEOUT = 10_000;
// With no time to start from, a box serves its newest events up to this.
const NO_SINCE_LIMIT = 500;

export class NostrMailbox implements Mailbox {
	constructor(
		private readonly urls: string[],
		private readonly tag: string,
		// serverPubkey: only its events count as the server's.
		private readonly serverPubkey: string
	) {}

	boxes() {
		return this.urls;
	}

	prepare(kind: MessageKind, body: string, tags: string[][] = []): Prepared {
		const ev = signedEvent(KINDS[kind], this.tag, body, tags);
		return { id: ev.id, raw: ev };
	}

	send(box: string, msg: Prepared): Promise<Posted> {
		const ev = msg.raw as NostrEvent;
		return new Promise((resolve, reject) => {
			let ws: WebSocket;
			const done = (err?: string) => {
				clearTimeout(timer);
				try {
					ws?.close();
				} catch {}
				if (err) reject(new Error(err));
				else resolve({ id: ev.id, box });
			};
			const timer = setTimeout(() => done(`${box}: no answer`), POST_TIMEOUT);
			try {
				ws = new WebSocket(box);
			} catch (e) {
				done(`${box}: ${String(e)}`);
				return;
			}
			ws.onopen = () => ws.send(JSON.stringify(['EVENT', ev]));
			ws.onmessage = (m) => {
				try {
					const msg = JSON.parse(String(m.data));
					if (msg[0] === 'OK' && msg[1] === ev.id) done(msg[2] ? undefined : `${box} refused: ${msg[3] ?? ''}`);
				} catch {}
			};
			ws.onerror = () => done(`${box}: connection failed`);
			ws.onclose = () => done(`${box}: closed`);
		});
	}

	post(box: string, kind: MessageKind, body: string, tags: string[][] = []): Promise<Posted> {
		return this.send(box, this.prepare(kind, body, tags));
	}

	// subscribe keeps a subscription open on a box, reconnecting with a
	// backoff; a box that fails three times in a row, or that refuses the
	// subscription (CLOSED, or a NOTICE before any event or EOSE), is told
	// down once.
	private subscribe(box: string, filter: Record<string, unknown>, each: (ev: NostrEvent, live: boolean) => void, down?: () => void): () => void {
		let ws: WebSocket | null = null;
		let stopped = false;
		let retry = 1000;
		let failures = 0;
		let served = false;
		let caughtUp = false;
		const sub = 's' + Math.random().toString(36).slice(2, 10);
		const refused = (why: string) => {
			console.warn('vote: box refused the subscription', box, why);
			// Counts as the last of three failures.
			failures = Math.max(failures, 2);
			try {
				ws?.close();
			} catch {}
		};
		const open = () => {
			if (stopped) return;
			served = false;
			caughtUp = false;
			try {
				ws = new WebSocket(box);
			} catch {
				schedule();
				return;
			}
			ws.onopen = () => {
				retry = 1000;
				ws?.send(JSON.stringify(['REQ', sub, { '#t': [this.tag], ...filter }]));
			};
			ws.onmessage = (m) => {
				try {
					const msg = JSON.parse(String(m.data));
					if (msg[0] === 'EVENT' && msg[1] === sub && msg[2]) {
						served = true;
						failures = 0;
						each(msg[2] as NostrEvent, caughtUp);
					} else if (msg[0] === 'EOSE' && msg[1] === sub) {
						served = true;
						caughtUp = true;
						failures = 0;
					} else if (msg[0] === 'CLOSED' && msg[1] === sub) refused(String(msg[2] ?? 'closed'));
					else if (msg[0] === 'NOTICE' && !served) refused(String(msg[1] ?? 'notice'));
				} catch {}
			};
			ws.onclose = () => schedule();
			ws.onerror = () => ws?.close();
		};
		const schedule = () => {
			if (stopped) return;
			if (++failures === 3 && down) {
				down();
				if (stopped) return;
			}
			setTimeout(open, retry);
			retry = Math.min(retry * 2, 30_000);
		};
		open();
		return () => {
			stopped = true;
			try {
				ws?.close();
			} catch {}
		};
	}

	serverMessages(box: string, kind: ServerKind, since: number, each: (m: ServerMessage) => void, down?: () => void): () => void {
		const filter: Record<string, unknown> = { kinds: [KINDS[kind]], authors: [this.serverPubkey] };
		if (since > 0) filter.since = since;
		else filter.limit = NO_SINCE_LIMIT;
		return this.subscribe(
			box,
			filter,
			(ev, live) => {
				if (ev.pubkey !== this.serverPubkey) return;
				// A box could forge an event under the server's key: only a
				// signature of that key counts (SPEC §7.2).
				if (!verifyEvent(ev)) {
					console.warn('vote: a server message that does not verify', box, kind, ev.id);
					return;
				}
				each({ kind, body: ev.content, time: ev.created_at, event: ev, live });
			},
			down
		);
	}

	registrations(box: string, since: number, each: (m: RegisterMessage) => void): () => void {
		return this.subscribe(box, { kinds: [KINDS.register], since }, (ev) => each({ id: ev.id, body: ev.content, time: ev.created_at }));
	}

	// A link names its receiver in a t tag (relays validate p tags as
	// 32-byte keys; the receiver's is a 33-byte compressed point).
	links(box: string, receiver: string, since: number, each: (sealed: string) => void): () => void {
		return this.subscribe(box, { kinds: [KINDS.link], '#t': [linkTag(receiver)], since }, (ev) => each(ev.content));
	}

	fetchEvent(id: string, timeoutMs: number): Promise<NostrEvent | null> {
		return new Promise((resolve) => {
			const stops: (() => void)[] = [];
			let done = false;
			const finish = (ev: NostrEvent | null) => {
				if (done) return;
				done = true;
				stops.forEach((s) => s());
				resolve(ev);
			};
			for (const box of this.urls) stops.push(this.subscribe(box, { ids: [id] }, (ev) => ev.id === id && verifyEvent(ev) && finish(ev)));
			setTimeout(() => finish(null), timeoutMs);
		});
	}
}

// relayRanking orders the boxes for a key by rendezvous hashing (SPEC
// §7.1): rank 0 is the registration relay, the first other non-retired
// one is the sticky relay for ballots.
export function relayRanking(scores: (box: string) => bigint, boxes: string[]): string[] {
	return [...boxes].sort((a, b) => (scores(a) < scores(b) ? -1 : scores(a) > scores(b) ? 1 : a < b ? -1 : 1));
}
