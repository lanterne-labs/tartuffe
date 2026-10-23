// Ballots go out through public Nostr relays (backend/polls/nostr.go): the
// browser publishes each one as an event to relays run by others, and our
// server reads them there at each hourly batch. So our server never sees the
// address a ballot comes from, and a ballot it would drop stays on the
// relays, where anyone finds it. Each event is signed by a key made for it
// alone, and dated to the hour it is sent in: nothing ties two ballots, or a
// ballot and a page view, together.

import { schnorr } from '@noble/curves/secp256k1.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex } from '@noble/hashes/utils.js';

// NOSTR_KIND and NOSTR_TAG match backend/polls (NostrKind, NostrTag).
export const NOSTR_KIND = 7464;
export const NOSTR_TAG = 'tartuffe-ballot-v1';

export interface NostrEvent {
	id: string;
	pubkey: string;
	created_at: number;
	kind: number;
	tags: string[][];
	content: string;
	sig: string;
}

// ballotEvent signs the ballot (its JSON) as an event of a fresh key.
export function ballotEvent(content: string, now = Date.now()): NostrEvent {
	const secret = schnorr.utils.randomSecretKey();
	const pubkey = bytesToHex(schnorr.getPublicKey(secret));
	const created_at = Math.floor(now / 3_600_000) * 3600;
	const tags = [['t', NOSTR_TAG]];
	const serial = JSON.stringify([0, pubkey, created_at, NOSTR_KIND, tags, content]);
	const id = sha256(new TextEncoder().encode(serial));
	return { id: bytesToHex(id), pubkey, created_at, kind: NOSTR_KIND, tags, content, sig: bytesToHex(schnorr.sign(id, secret)) };
}

// RELAY_TIMEOUT bounds the wait for one relay's answer.
const RELAY_TIMEOUT = 8000;

// sendTo publishes the event to one relay and tells whether it took it.
function sendTo(relay: string, ev: NostrEvent): Promise<boolean> {
	return new Promise((resolve) => {
		let ws: WebSocket;
		const done = (ok: boolean) => {
			clearTimeout(timer);
			try {
				ws?.close();
			} catch {}
			resolve(ok);
		};
		const timer = setTimeout(() => done(false), RELAY_TIMEOUT);
		try {
			ws = new WebSocket(relay);
		} catch {
			done(false);
			return;
		}
		ws.onopen = () => ws.send(JSON.stringify(['EVENT', ev]));
		ws.onmessage = (m) => {
			try {
				const msg = JSON.parse(String(m.data));
				if (msg[0] === 'OK' && msg[1] === ev.id) {
					if (!msg[2]) console.warn('relay refused the ballot', relay, msg[3]);
					done(!!msg[2]);
				}
			} catch {}
		};
		ws.onerror = () => done(false);
		ws.onclose = () => done(false);
	});
}

// publish sends the event to every relay at once and returns those that took
// it.
export async function publish(relays: string[], ev: NostrEvent): Promise<string[]> {
	const answers = await Promise.all(relays.map((r) => sendTo(r, ev)));
	return relays.filter((_, i) => answers[i]);
}
