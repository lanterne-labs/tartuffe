// The voter's side of the protocol (SPEC §4, §6, §7.2): registering, the
// acknowledgements, casting a ballot, the receipt. Pure logic over the
// Mailbox and the Sources; the UI drives it and shows its states.

import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex } from '@noble/hashes/utils.js';
import { compress, hex, big, leafOf, nullifierOf, registrationBinding, values, pack, type Ranking, validRanking } from './crypto';
import type { Mailbox, Posted, NostrEvent } from './mailbox';
import type { Sources } from './sources';
import type { VoteKey } from './key';
import type { Prover } from './prove';

// Setup is the setup entry in force, as the mirror serves it.
export interface Setup {
	seq: number;
	setup: {
		v: number;
		event: number;
		options: { slot: number; kind: string; person_id?: number; name?: string }[];
		mask: number;
		nostr_pubkey: string;
		relays: { url: string; state: string }[];
		k_real: number;
		anchor_age: number;
		checkpoint_period: number;
		validity: number;
		closes: string;
		blackouts: { from: string; to: string }[];
	};
}

// Ack is the server's acknowledgement of a message.
export interface Ack {
	v: number;
	nostr_id: string;
	body_hash: string;
	pi_hash?: string;
	checkpoint: number;
	outcome: 'logged' | 'rejected';
	reason?: string;
}

// Checkpoint is a signed note's content (its signature is checked by
// votelog.openCheckpoint; the lines are read here).
export interface CheckpointInfo {
	note: string;
	logSize: number;
	treeRoot: string;
	treeSize: number;
	index: number;
	event: number;
	time: number;
	mirror: string;
}

export function parseCheckpoint(note: string): CheckpointInfo | null {
	const text = note.split('\n\n')[0];
	const lines = text.split('\n');
	const info: CheckpointInfo = { note, logSize: Number(lines[1]), treeRoot: '', treeSize: 0, index: 0, event: 0, time: 0, mirror: '' };
	for (const l of lines.slice(3)) {
		const f = l.split(' ');
		if (f[0] === 'tree') {
			info.treeRoot = f[1];
			info.treeSize = Number(f[2]);
		} else if (f[0] === 'index') info.index = Number(f[1]);
		else if (f[0] === 'event') info.event = Number(f[1]);
		else if (f[0] === 'time') info.time = Number(f[1]);
		else if (f[0] === 'mirror') info.mirror = f[1];
	}
	return info.treeRoot && info.index ? info : null;
}

export const bodyHash = (body: string) => '0x' + bytesToHex(sha256(new TextEncoder().encode(body)));

// SignedAck is an acknowledgement with the server's signed event: the
// proof a complaint carries (SPEC §7.2).
export interface SignedAck {
	ack: Ack;
	event: NostrEvent;
}

// waitAck waits for the server's acknowledgement of a posted message on
// its box, up to the timeout.
export function waitAck(mail: Mailbox, posted: Posted, timeoutMs: number): Promise<SignedAck | null> {
	return new Promise((resolve) => {
		const since = Math.floor(Date.now() / 1000) - 120;
		let done = false;
		const stop = mail.serverMessages(posted.box, 'ack', since, (m) => {
			try {
				const ack = JSON.parse(m.body) as Ack;
				if (ack.nostr_id === posted.id && !done) {
					done = true;
					stop();
					resolve({ ack, event: m.event });
				}
			} catch {}
		});
		setTimeout(() => {
			if (!done) {
				done = true;
				stop();
				resolve(null);
			}
		}, timeoutMs);
	});
}

// postWithAck posts to the key's box and waits for the acknowledgement,
// moving to the next box of the ranking when a box fails or stays silent.
export async function postWithAck(
	mail: Mailbox,
	boxes: string[],
	kind: 'ballot' | 'register',
	body: string,
	timeoutMs: number
): Promise<{ posted: Posted; ack: Ack; ackEvent: NostrEvent }> {
	let lastErr = 'no box answered';
	let reposts = 0;
	for (const box of boxes) {
		let posted: Posted;
		try {
			posted = await mail.post(box, kind, body);
		} catch (e) {
			lastErr = e instanceof Error ? e.message : String(e);
			continue;
		}
		const signed = await waitAck(mail, posted, timeoutMs);
		if (signed) {
			const { ack, event: ackEvent } = signed;
			// A repost of our own body that the server already took is a
			// success (SPEC §7.2): the first box's acknowledgement was missed.
			if (reposts > 0 && ack.outcome === 'rejected' && (ack.reason === 'duplicate' || ack.reason === 'uid_used' || ack.reason === 'id_used')) {
				return { posted, ack: { ...ack, outcome: 'logged', reason: undefined }, ackEvent };
			}
			return { posted, ack, ackEvent };
		}
		lastErr = `${box}: no acknowledgement`;
		reposts++;
	}
	throw new Error(lastErr);
}

// --- Registration ---

export interface RegisterBody {
	v: 2;
	event: number;
	id: string;
	uid: string;
	proof: unknown;
	proof_date: string;
}

// registrationRequest is what the ZKPassport flow needs for a key: the
// bound custom data and the scope.
export function registrationRequest(key: VoteKey, slug: string): { customData: string; scope: string } {
	return { customData: registrationBinding(BigInt(key.eventId), big(key.leaf)), scope: `tartuffe-vote/2:${slug}` };
}

export function registerBody(key: VoteKey, uid: string, proof: unknown, proofDate: Date): string {
	const body: RegisterBody = { v: 2, event: key.eventId, id: key.leaf, uid, proof, proof_date: proofDate.toISOString() };
	return canonical(body);
}

// --- Ballots ---

export interface BallotBody {
	v: 2;
	event: number;
	setup: number;
	root: string;
	nullifier: string;
	seq: number;
	c0: string;
	c: string[];
	proof: string;
}

// makeBallot proves a ranking and returns the body to post and its
// public-input hash (what the acknowledgement names).
export async function makeBallot(prover: Prover, key: VoteKey, setup: Setup, path: string[], root: string, seq: number, r: Ranking): Promise<{ body: string; piHash: string }> {
	const bad = validRanking(r, setup.setup.mask);
	if (bad) throw new Error(bad);
	const secret = big(key.secret);
	const event = BigInt(key.eventId);
	const nullifier = nullifierOf(event, secret);
	const rnd = randomNonzero();
	const inputs = {
		event: hex(event),
		setup: hex(BigInt(setup.seq)),
		mask: setup.setup.mask,
		root,
		nullifier: hex(nullifier),
		seq,
		s: key.secret,
		path,
		index: key.position,
		slots: r.slots,
		len: r.len,
		r: hex(rnd)
	};
	const { proof, publicInputs, returnValue } = await prover.prove(inputs);
	const pts = (returnValue as string[]).map(big);
	const c0 = compress(pts[0], pts[1]);
	const c: string[] = [];
	for (let j = 0; j < 10; j++) c.push(compress(pts[2 + 2 * j], pts[3 + 2 * j]));
	const body: BallotBody = { v: 2, event: key.eventId, setup: setup.seq, root, nullifier: hex(nullifier), seq, c0, c, proof: b64(proof) };
	// pi_hash: SHA-256 over the public inputs as 32-byte fields, in order.
	const pi = new Uint8Array(32 * publicInputs.length);
	publicInputs.forEach((p, i) => pi.set(toBytes32(big(p)), 32 * i));
	return { body: canonical(body), piHash: '0x' + bytesToHex(sha256(pi)) };
}

function toBytes32(x: bigint): Uint8Array {
	const out = new Uint8Array(32);
	for (let i = 31; i >= 0; i--) {
		out[i] = Number(x & 0xffn);
		x >>= 8n;
	}
	return out;
}

function randomNonzero(): bigint {
	const b = new Uint8Array(32);
	crypto.getRandomValues(b);
	const x = BigInt('0x' + bytesToHex(b)) % 21888242871839275222246405745257275088548364400416034343698204186575808495617n;
	return x === 0n ? 1n : x;
}

function b64(b: Uint8Array): string {
	let s = '';
	for (let i = 0; i < b.length; i += 0x8000) s += String.fromCharCode(...b.subarray(i, i + 0x8000));
	return btoa(s);
}

// canonical sorts keys and drops spaces, as the server's Canonical.
export function canonical(v: unknown): string {
	const sort = (x: unknown): unknown => {
		if (Array.isArray(x)) return x.map(sort);
		if (x && typeof x === 'object') {
			const o: Record<string, unknown> = {};
			for (const k of Object.keys(x as object).sort()) o[k] = sort((x as Record<string, unknown>)[k]);
			return o;
		}
		return x;
	};
	return JSON.stringify(sort(v));
}

// complaintBody is what the browser posts when its acknowledged ballot is
// not in the log: the server's signed ack, for anyone to judge against
// the log (SPEC §7.2, §12.6).
export function complaintBody(eventId: number, nostrId: string, piHash: string, ackEvent: NostrEvent): string {
	return canonical({ v: 2, event: eventId, nostr_id: nostrId, pi_hash: piHash, ack: ackEvent });
}

// --- The receipt ---

export interface MirrorEntry {
	checkpoint: number;
	kind: string;
	hash: string;
	log_seq: number;
}

// findReceipt looks for a hash in the entries files of the hours from the
// post on (the same files every visitor fetches).
export async function findReceipt(src: Sources, token: string, hash: string, postedAt: Date): Promise<MirrorEntry | null> {
	const hours: string[] = [];
	for (let t = postedAt.getTime() - 3_600_000; t <= Date.now() + 3_600_000; t += 3_600_000) hours.push(new Date(t).toISOString().slice(0, 13));
	for (const hr of hours) {
		const list = await src.json<MirrorEntry[]>(`entries/${hr}.json`, token);
		const found = list?.find((e) => e.hash === hash);
		if (found) return found;
	}
	return null;
}

// myPosition finds the key's leaf among the leaves of the day (the file
// everyone fetches).
export async function myPosition(src: Sources, token: string, leaf: string, day: Date): Promise<number | null> {
	for (const d of [day, new Date(day.getTime() + 86_400_000)]) {
		const list = await src.json<{ position: number; id: string }[]>(`leaves/${d.toISOString().slice(0, 10)}.json`, token);
		const found = list?.find((l) => l.id === leaf);
		if (found) return found.position;
	}
	return null;
}

export { leafOf, values, pack };
