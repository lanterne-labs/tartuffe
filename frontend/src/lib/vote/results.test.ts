import { describe, expect, it } from 'vitest';
import { rankedRows, byFirst } from './results';
import type { RankedResults } from '@isoloir/web';

const r: RankedResults = {
	type: 'ranked',
	counted: 3,
	ballots: 'voters',
	slots: [
		{ slot: 0, label: 'A', blank: false, first: 1, ranked: 3 },
		{ slot: 1, label: 'B', blank: false, first: 2, ranked: 2 },
		{ slot: 2, label: 'C', blank: false, first: 0, ranked: 0 },
		{ slot: 3, label: null, blank: true, first: 0, ranked: 0 }
	],
	margins: [
		[0, 1, 3, 3],
		[-1, 0, 2, 2],
		[-3, -2, 0, 0],
		[-3, -2, 0, 0]
	],
	condorcet: 0
};

describe('the results of a ranked poll', () => {
	it('ranks by head-to-head wins, sharing places on ties', () => {
		const s = rankedRows(r)!;
		expect(s.counted).toBe(3);
		expect(s.condorcet).toBe(0);
		expect(s.rows.map((x) => [x.slot, x.rank])).toEqual([
			[0, 1],
			[1, 2],
			[2, 3],
			[3, 3]
		]);
		expect(byFirst(s.rows).map((x) => x.slot)).toEqual([1, 0, 2, 3]);
	});

	it('reads nothing but ranked results', () => {
		expect(rankedRows(null)).toBeNull();
		expect(rankedRows({ type: 'choice', counted: 0, slots: [], blank: null, ballots: 'voters', none: 0 })).toBeNull();
	});
});
