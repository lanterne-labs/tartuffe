import { describe, expect, it } from 'vitest';
import vectors from './testdata/votelog-vectors.json';
import { leafData, leafHash } from './votelog';
import { count, schulze, treeHash } from './recount';

const b64 = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
const leaves = vectors.entries.map((e) => leafHash(leafData(e.seq, e.kind, e.body)));

describe('the recount', () => {
	it('rebuilds the tree hash of every head of Go’s log', () => {
		for (const h of vectors.heads) {
			expect(treeHash(leaves.slice(0, h.size)), `size ${h.size}`).toEqual(b64(h.hash));
		}
	});

	it('counts as polls.Tally: the last choices, blanks, a left candidate skipped', () => {
		const c = count(
			[1, 2, 3],
			[
				{ kind: 'choices', choices: [1, 2] },
				{ kind: 'choices', choices: [9, 2] },
				{ kind: 'blank', choices: [] },
				{ kind: 'withdraw', choices: [] },
				{ kind: 'choices', choices: [9] }
			]
		);
		expect(c.voters).toBe(3);
		expect(c.blank).toBe(1);
		expect([...c.first]).toEqual([[1, 1], [2, 1], [3, 0]]);
		expect([...c.ranked]).toEqual([[1, 1], [2, 2], [3, 0]]);
		// 1 above 2 and 3 on the first ballot; 2 above 3 on both.
		expect(c.beats.get(1)!.get(2)).toBe(1);
		expect(c.beats.get(2)!.get(1)).toBe(1);
		expect(c.beats.get(2)!.get(3)).toBe(2);
	});

	it('finds the Condorcet winner, and none in a cycle', () => {
		const ballots = (rows: [number, number[]][]) => rows.flatMap(([n, choices]) => Array.from({ length: n }, () => ({ kind: 'choices' as const, choices })));
		const win = count([1, 2, 3], ballots([[4, [1, 2, 3]], [3, [2, 1, 3]], [2, [3, 1, 2]]]));
		expect(schulze([1, 2, 3], win.beats).winner).toBe(1);
		const cycle = count([1, 2, 3], ballots([[1, [1, 2, 3]], [1, [2, 3, 1]], [1, [3, 1, 2]]]));
		const s = schulze([1, 2, 3], cycle.beats);
		expect(s.winner).toBeNull();
		expect([...s.wins.values()]).toEqual([0, 0, 0]);
	});
});
