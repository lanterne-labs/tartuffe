// The mailbox (SPEC §7): where the browser posts registrations and ballots
// and reads the server's acknowledgements and checkpoints. A set of named
// boxes anyone can read; today Nostr relays, one box per relay URL. The
// vote code depends on the Mailbox interface only: another transport is
// one more implementation here, with its twin in backend/vote/mailbox.go.

import { schnorr } from '@noble/curves/secp256k1.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex } from '@noble/hashes/utils.js';

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
	// box since the time given, live, until the subscription is stopped.
	serverMessages(box: string, kind: ServerKind, since: number, each: (m: ServerMessage) => void): () => void;
	// registrations reads the voters' registrations posted to the box
	// since the time given, live, until stopped (the newest leaves, before
	// the mirror has them).
	registrations(box: string, since: number, each: (m: RegisterMessage) => void): () => void;
}

// --- Nostr ---

// The kinds of backend/vote/nostr.go.
const KINDS: Record<MessageKind | ServerKind, number> = { ballot: 17020, register: 17021, ack: 17022, checkpoint: 17023, complaint: 17024, snapshot: 17025 };

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

	private subscribe(box: string, filter: Record<string, unknown>, each: (ev: NostrEvent) => void): () => void {
		let ws: WebSocket | null = null;
		let stopped = false;
		let retry = 1000;
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

	serverMessages(box: string, kind: ServerKind, since: number, each: (m: ServerMessage) => void): () => void {
		return this.subscribe(box, { kinds: [KINDS[kind]], authors: [this.serverPubkey], since }, (ev) => {
			if (ev.pubkey !== this.serverPubkey) return;
			each({ kind, body: ev.content, time: ev.created_at });
		});
	}

	registrations(box: string, since: number, each: (m: RegisterMessage) => void): () => void {
		return this.subscribe(box, { kinds: [KINDS.register], since }, (ev) => each({ id: ev.id, body: ev.content, time: ev.created_at }));
	}
}

// relayRanking orders the boxes for a key by rendezvous hashing (SPEC
// §7.1): rank 0 is the registration relay, the first other non-retired
// one is the sticky relay for ballots.
export function relayRanking(scores: (box: string) => bigint, boxes: string[]): string[] {
	return [...boxes].sort((a, b) => (scores(a) < scores(b) ? -1 : scores(a) > scores(b) ? 1 : a < b ? -1 : 1));
}
