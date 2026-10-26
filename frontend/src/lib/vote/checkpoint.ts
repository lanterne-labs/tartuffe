// The log's checkpoints, checked in the browser (backend/polls/checkpoint.go,
// votelog.go): the log is a Merkle tree, as Certificate Transparency's
// (RFC 6962), and the ingest signs its head with its extension lines. The
// vote session checks each checkpoint's signature with the pinned key.

import { ed25519 } from '@noble/curves/ed25519.js';
import { sha256 } from '@noble/hashes/sha2.js';

export const LOG_ORIGIN = 'tartuffe.me/vote-log';

export interface Tree {
	size: number;
	hash: Uint8Array;
}

const b64 = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
const toB64 = (b: Uint8Array) => btoa(String.fromCharCode(...b));
const utf8 = (s: string) => new TextEncoder().encode(s);

function concat(...parts: Uint8Array[]): Uint8Array {
	const out = new Uint8Array(parts.reduce((n, p) => n + p.length, 0));
	let i = 0;
	for (const p of parts) {
		out.set(p, i);
		i += p.length;
	}
	return out;
}

function equal(a: Uint8Array, b: Uint8Array): boolean {
	return a.length === b.length && a.every((x, i) => x === b[i]);
}

// leafData is the record of a log entry (polls.LeafData).
export function leafData(seq: number, kind: string, body: string): Uint8Array {
	return utf8(`tartuffe-log/v1\n${seq}\n${kind}\n${body}`);
}

export const leafHash = (data: Uint8Array) => sha256(concat(new Uint8Array([0]), data));
const nodeHash = (l: Uint8Array, r: Uint8Array) => sha256(concat(new Uint8Array([1]), l, r));

// verifyInclusion checks the proof that the tree holds the leaf at index
// (RFC 9162, 2.1.3.2).
export function verifyInclusion(index: number, tree: Tree, leaf: Uint8Array, proof: Uint8Array[]): boolean {
	if (index >= tree.size) return false;
	let fn = index;
	let sn = tree.size - 1;
	let r = leaf;
	for (const p of proof) {
		if (sn === 0) return false;
		if (fn % 2 === 1 || fn === sn) {
			r = nodeHash(p, r);
			if (fn % 2 === 0) {
				while (fn % 2 === 0 && fn !== 0) {
					fn = Math.floor(fn / 2);
					sn = Math.floor(sn / 2);
				}
			}
		} else {
			r = nodeHash(r, p);
		}
		fn = Math.floor(fn / 2);
		sn = Math.floor(sn / 2);
	}
	return sn === 0 && equal(r, tree.hash);
}

// verifyConsistency checks the proof that the newer tree extends the older
// one (RFC 9162, 2.1.4.2).
export function verifyConsistency(older: Tree, newer: Tree, proof: Uint8Array[]): boolean {
	if (older.size > newer.size) return false;
	if (older.size === newer.size) return proof.length === 0 && equal(older.hash, newer.hash);
	if (older.size === 0) return proof.length === 0;
	if (proof.length === 0) return false;
	let path = proof;
	if ((older.size & (older.size - 1)) === 0) path = [older.hash, ...proof];
	let fn = older.size - 1;
	let sn = newer.size - 1;
	while (fn % 2 === 1) {
		fn = Math.floor(fn / 2);
		sn = Math.floor(sn / 2);
	}
	let fr = path[0];
	let sr = path[0];
	for (const c of path.slice(1)) {
		if (sn === 0) return false;
		if (fn % 2 === 1 || fn === sn) {
			fr = nodeHash(c, fr);
			sr = nodeHash(c, sr);
			if (fn % 2 === 0) {
				while (fn % 2 === 0 && fn !== 0) {
					fn = Math.floor(fn / 2);
					sn = Math.floor(sn / 2);
				}
			}
		} else {
			sr = nodeHash(sr, c);
		}
		fn = Math.floor(fn / 2);
		sn = Math.floor(sn / 2);
	}
	return sn === 0 && equal(fr, older.hash) && equal(sr, newer.hash);
}

// openCheckpoint checks a checkpoint (a signed note, polls.CheckpointText)
// against the verifier key <name>+<hex key hash>+<base64 0x01 || key> and
// returns its tree, or null.
export function openCheckpoint(note: string, vkey: string): Tree | null {
	// The base64 key may hold '+' too: it is all after the second one.
	const [name, hashHex, ...rest] = vkey.split('+');
	const keyB64 = rest.join('+');
	if (!name || !hashHex || !keyB64) return null;
	const key = b64(keyB64);
	if (key.length !== 33 || key[0] !== 1) return null;
	const sep = note.lastIndexOf('\n\n');
	if (sep < 0) return null;
	const text = note.slice(0, sep + 1);
	const keyHash = sha256(concat(utf8(name + '\n'), key)).slice(0, 4);
	let ok = false;
	for (const line of note.slice(sep + 2).split('\n')) {
		const m = line.match(/^— (\S+) (\S+)$/);
		if (!m || m[1] !== name) continue;
		const sig = b64(m[2]);
		if (sig.length !== 68 || !equal(sig.slice(0, 4), keyHash)) continue;
		try {
			ok = ed25519.verify(sig.slice(4), utf8(text), key.slice(1));
		} catch {
			ok = false;
		}
		if (ok) break;
	}
	if (!ok) return null;
	const lines = text.split('\n');
	if (lines[0] !== LOG_ORIGIN || !/^\d+$/.test(lines[1] ?? '')) return null;
	const hash = b64(lines[2] ?? '');
	return hash.length === 32 ? { size: Number(lines[1]), hash } : null;
}

// --- What this device keeps ---

interface Seen {
	key: string;
	size: number;
	hash: string;
	note: string;
}
