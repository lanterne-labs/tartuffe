// The mailbox (SPEC §7): where the browser posts registrations and ballots
// and reads the server's acknowledgements and checkpoints. A set of named
// boxes anyone can read; today Nostr relays, one box per relay URL. The
// vote code depends on the Mailbox interface only: another transport is
// one more implementation here, with its twin in backend/vote/mailbox.go.

import { schnorr } from '@noble/curves/secp256k1.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex, hexToBytes } from '@noble/hashes/utils.js';
import { bech32 } from '@scure/base';

export type MessageKind = 'ballot' | 'register' | 'complaint';
export type ServerKind = 'ack' | 'checkpoint' | 'snapshot';

// Posted is the receipt of a post: the message's id in the box.
export interface Posted {
	id: string;
	box: string;
}

// ServerMessage is one the server posted to a box.
export interface ServerMessage {
	kind: ServerKind;
	body: string;
	time: number;
	// event: the signed event itself (a complaint carries the ack's).
	event: NostrEvent;
}

// Registration is a registration as read from a box (the newest leaves).
export interface RegisterMessage {
	id: string;
	body: string;
	time: number;
}

export interface Mailbox {
	boxes(): string[];
	// post sends a voter message to one box; it resolves once the box took
	// it, or rejects.
	post(box: string, kind: MessageKind, body: string): Promise<Posted>;
	// serverMessages reads the server's messages of a kind posted to the
	// box since the time given, live, until the subscription is stopped;
	// down is told once when the box cannot be reached three times in a
	// row (the caller moves to another box).
	serverMessages(box: string, kind: ServerKind, since: number, each: (m: ServerMessage) => void, down?: () => void): () => void;
	// registrations reads the voters' registrations posted to the box
	// since the time given, live, until stopped (the newest leaves, before
	// the mirror has them).
	registrations(box: string, since: number, each: (m: RegisterMessage) => void): () => void;
	// fetchEvent reads one event by its id from whichever box holds it
	// (a complaint someone links to); null when none does in time.
	fetchEvent(id: string, timeoutMs: number): Promise<NostrEvent | null>;
}

// --- Nostr ---

// The kinds of backend/vote/nostr.go.
const KINDS: Record<MessageKind | ServerKind, number> = { ballot: 7020, register: 7021, ack: 7022, checkpoint: 7023, complaint: 7024, snapshot: 7025 };

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
// dated to the minute.
function signedEvent(kind: number, tag: string, content: string, now = Date.now()): NostrEvent {
	const secret = schnorr.utils.randomSecretKey();
	const pubkey = bytesToHex(schnorr.getPublicKey(secret));
	const created_at = Math.floor(now / 60_000) * 60;
	const tags = [['t', tag]];
	const serial = JSON.stringify([0, pubkey, created_at, kind, tags, content]);
	const id = sha256(new TextEncoder().encode(serial));
	return { id: bytesToHex(id), pubkey, created_at, kind, tags, content, sig: bytesToHex(schnorr.sign(id, secret)) };
}

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

	post(box: string, kind: MessageKind, body: string): Promise<Posted> {
		const ev = signedEvent(KINDS[kind], this.tag, body);
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

	private subscribe(box: string, filter: Record<string, unknown>, each: (ev: NostrEvent) => void, down?: () => void): () => void {
		let ws: WebSocket | null = null;
		let stopped = false;
		let retry = 1000;
		let failures = 0;
		const sub = 's' + Math.random().toString(36).slice(2, 10);
		const open = () => {
			if (stopped) return;
			try {
				ws = new WebSocket(box);
			} catch {
				schedule();
				return;
			}
			ws.onopen = () => {
				retry = 1000;
				failures = 0;
				ws?.send(JSON.stringify(['REQ', sub, { ...filter, '#t': [this.tag] }]));
			};
			ws.onmessage = (m) => {
				try {
					const msg = JSON.parse(String(m.data));
					if (msg[0] === 'EVENT' && msg[1] === sub && msg[2]) each(msg[2] as NostrEvent);
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
		return this.subscribe(
			box,
			{ kinds: [KINDS[kind]], authors: [this.serverPubkey], since },
			(ev) => {
				if (ev.pubkey !== this.serverPubkey) return;
				each({ kind, body: ev.content, time: ev.created_at, event: ev });
			},
			down
		);
	}

	registrations(box: string, since: number, each: (m: RegisterMessage) => void): () => void {
		return this.subscribe(box, { kinds: [KINDS.register], since }, (ev) => each({ id: ev.id, body: ev.content, time: ev.created_at }));
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
