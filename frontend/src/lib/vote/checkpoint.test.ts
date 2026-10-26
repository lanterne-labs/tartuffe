import { describe, expect, it } from 'vitest';
import vectors from './testdata/votelog-vectors.json';
import { leafData, leafHash, openCheckpoint, verifyConsistency, verifyInclusion, type Tree } from './checkpoint';

// The vectors are proofs of Go's tlog (backend/polls/vectors_test.go).
const b64 = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
const heads: Tree[] = vectors.heads.map((h) => ({ size: h.size, hash: b64(h.hash) }));
const head = (n: number) => heads[n - 1];
const leaves = vectors.entries.map((e) => leafHash(leafData(e.seq, e.kind, e.body)));

describe('the vote log checks', () => {
	it('verify every inclusion proof, and refuse a wrong leaf', () => {
		for (const p of vectors.inclusion) {
			const proof = p.hashes.map(b64);
			expect(verifyInclusion(p.at, head(p.size), leaves[p.at], proof), `${p.at} in ${p.size}`).toBe(true);
			expect(verifyInclusion(p.at, head(p.size), leaves[(p.at + 1) % 13], proof)).toBe(false);
		}
	});

	it('verify every consistency proof, and refuse a changed history', () => {
		for (const p of vectors.consistency) {
			const proof = p.hashes.map(b64);
			expect(verifyConsistency(head(p.at), head(p.size), proof), `${p.at} to ${p.size}`).toBe(true);
			expect(verifyConsistency({ size: p.at, hash: leaves[12] }, head(p.size), proof)).toBe(false);
		}
		expect(verifyConsistency(head(5), head(9), vectors.consistency.find((p) => p.at === 4 && p.size === 9)!.hashes.map(b64))).toBe(false);
	});

	it('open the signed checkpoint with its key only', () => {
		const t = openCheckpoint(vectors.checkpoint, vectors.key);
		expect(t?.size).toBe(13);
		expect(t?.hash).toEqual(head(13).hash);
		expect(openCheckpoint(vectors.checkpoint.replace('\n13\n', '\n14\n'), vectors.key)).toBeNull();
		const other = vectors.key.replace(/\+[^+]+$/, '+' + btoa(String.fromCharCode(1, ...new Uint8Array(32).fill(7))));
		expect(openCheckpoint(vectors.checkpoint, other)).toBeNull();
	});
});
