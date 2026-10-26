// The browser's arithmetic against the circuit's vectors
// (circuits/ballot/vectors/dev.json): the same hashes, leaves, pseudonyms,
// tree root and packed plaintexts as Noir and Go.
import { readFileSync } from 'node:fs';
import { describe, it, expect, beforeAll } from 'vitest';
import { bb, h, leafOf, nullifierOf, rootFromPath, values, pack, unpack, big, hex, T_ID, T_NULL, T_NODE, zeroHashes, node, compress } from './crypto';
import { relayRanking } from './mailbox';

const vectors = JSON.parse(readFileSync(new URL('../../../../circuits/ballot/vectors/dev.json', import.meta.url), 'utf8'));

describe('vote crypto', () => {
	beforeAll(async () => {
		await bb();
	});

	it('hashes as the circuit', () => {
		expect(hex(h(1n, 2n, 3n))).toBe(vectors.poseidon2.hash);
		expect(hex(T_ID)).toBe(vectors.tags.T_ID);
		expect(hex(T_NULL)).toBe(vectors.tags.T_NULL);
		expect(hex(T_NODE)).toBe(vectors.tags.T_NODE);
	});

	it('derives the leaf and the pseudonym', () => {
		const event = big(vectors.ballot.inputs.event);
		const s = big(vectors.ballot.inputs.s);
		expect(hex(leafOf(event, s))).toBe(vectors.tree.leaves[vectors.tree.index]);
		expect(hex(nullifierOf(event, s))).toBe(vectors.ballot.inputs.nullifier);
	});

	it('recomputes the root from the path', () => {
		const leaf = big(vectors.tree.leaves[vectors.tree.index]);
		const path = (vectors.tree.path as string[]).map(big);
		expect(hex(rootFromPath(leaf, path, vectors.tree.index))).toBe(vectors.tree.root);
		// The zero hashes are the empty subtrees of the path's upper levels.
		const zeros = zeroHashes();
		expect(hex(zeros[19])).toBe(vectors.tree.path[19]);
		expect(hex(node(0n, 0n))).toBe(hex(zeros[1]));
	});

	it('packs the values as the circuit', () => {
		const r = { len: vectors.ballot.inputs.len, slots: vectors.ballot.inputs.slots };
		const v = values(r);
		expect(v.map((x) => String(x + 1))).toEqual(vectors.ballot.digits);
		const p = pack(v);
		expect(p.map(hex)).toEqual(vectors.ballot.packed);
		expect(unpack(p, 1n)).toEqual(v);
	});

	it('compresses points with the parity bit', () => {
		const c = vectors.ballot.ciphertexts as string[];
		const x = big(c[0]);
		const y = big(c[1]);
		const enc = compress(x, y);
		expect(big(enc) & ((1n << 255n) - 1n)).toBe(x);
		expect((big(enc) >> 255n) === 1n).toBe((y & 1n) === 1n);
	});

	it('ranks relays deterministically', () => {
		const boxes = ['wss://b', 'wss://a', 'wss://c'];
		const scores = (b: string) => BigInt(b.length) * 1000n + BigInt(b.charCodeAt(6));
		expect(relayRanking(scores, boxes)).toEqual(['wss://a', 'wss://b', 'wss://c']);
	});
});
