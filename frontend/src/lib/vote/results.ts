// The results of a ranked poll as the pages show them: one row per slot
// (the candidates, then the blank), with its first-choice and ranked
// counts and its place in the ranking. The ranking orders the slots by
// how many others each beats head to head (the margins of the tally),
// then by the ballots that rank it, then by first choices; slots equal on
// all three share a place. The Condorcet winner is the tally's.

import type { RankedResults, Results } from '@isoloir/web';

export interface Row {
	slot: number;
	blank: boolean;
	first: number;
	ranked: number;
	wins: number;
	rank: number;
}

export interface Shown {
	counted: number;
	rows: Row[];
	condorcet: number | null;
}

export function rankedRows(r: Results | null | undefined): Shown | null {
	if (!r || r.type !== 'ranked') return null;
	const res = r as RankedResults;
	const rows: Row[] = res.slots.map((s) => {
		const m = res.margins?.[s.slot];
		const wins = m ? m.filter((x, j) => j !== s.slot && x > 0).length : 0;
		return { slot: s.slot, blank: s.blank, first: s.first, ranked: s.ranked, wins, rank: 0 };
	});
	rows.sort((a, b) => b.wins - a.wins || b.ranked - a.ranked || b.first - a.first || a.slot - b.slot);
	rows.forEach((row, i) => {
		const prev = rows[i - 1];
		row.rank = prev && prev.wins === row.wins && prev.ranked === row.ranked && prev.first === row.first ? prev.rank : i + 1;
	});
	return { counted: res.counted, rows, condorcet: res.condorcet ?? null };
}

// byFirst: the same rows by first-choice share.
export function byFirst(rows: Row[]): Row[] {
	return [...rows].sort((a, b) => b.first - a.first || a.rank - b.rank);
}
