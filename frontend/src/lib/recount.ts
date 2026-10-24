// Anyone recounts the results shown from the public log, in the browser
// (backend/polls/audit.go does it in full: dm poll verify). It checks the
// log's signed head, that the entries served make its Merkle tree, every
// ballot's proof of the results' window against a root the log published
// before it, then counts the last ballot of each voter as the server does
// (polls.Tally, polls.Schulze) and compares with the log's last tally and
// with the results on the page. The results name the log size they count
// (log_size): the recount reads exactly that many entries, so a batch
// published meanwhile changes nothing.

import { verifyProof, type SemaphoreProof } from '@semaphore-protocol/proof';
import { API_BASE } from './config';
import { digest, message, scopeName, type BallotKind } from './anonballot';
import { checkHead, leafData, leafHash, type Tree } from './votelog';
import { sha256 } from '@noble/hashes/sha2.js';
import type { PollResults } from './types';

export type StepId = 'signed' | 'tree' | 'proofs' | 'count' | 'shown';
export type StepState = 'running' | 'ok' | 'failed' | 'skipped';

export interface Step {
	id: StepId;
	state: StepState;
	// n: what the step counted (entries, proofs, voters).
	n?: number;
	detail?: string;
}

interface Entry {
	seq: number;
	kind: string;
	body: string;
}

interface LogBallot {
	poll: number;
	series: number;
	window: number;
	seq: number;
	kind: BallotKind;
	choices?: number[];
	proof: SemaphoreProof & { merkleTreeRoot: string; nullifier: string; message: string; scope: string };
}

interface TallyEntry {
	poll: number;
	series: number;
	window: number;
	voters: number;
	blank: number;
	first: Record<string, number>;
	ranked: Record<string, number>;
	beats: Record<string, Record<string, number>>;
	blank_ranked?: number;
	blank_beats?: Record<string, number>;
}

const nodeHash = (l: Uint8Array, r: Uint8Array) => {
	const b = new Uint8Array(1 + l.length + r.length);
	b[0] = 1;
	b.set(l, 1);
	b.set(r, 1 + l.length);
	return sha256(b);
};

// treeHash is the Merkle tree hash of the leaves (RFC 6962, 2.1).
export function treeHash(leaves: Uint8Array[]): Uint8Array {
	if (leaves.length === 0) return sha256(new Uint8Array());
	if (leaves.length === 1) return leaves[0];
	let k = 1;
	while (k * 2 < leaves.length) k *= 2;
	return nodeHash(treeHash(leaves.slice(0, k)), treeHash(leaves.slice(k)));
}

const equal = (a: Uint8Array, b: Uint8Array) => a.length === b.length && a.every((x, i) => x === b[i]);

async function entries(n: number): Promise<Entry[]> {
	const out: Entry[] = [];
	while (out.length < n) {
		const res = await fetch(`${API_BASE}/api/vote-log?after=${out.length}&limit=1000`, { credentials: 'omit' });
		if (!res.ok) throw new Error(`HTTP ${res.status}`);
		const page = (await res.json()) as Entry[];
		if (page.length === 0) break;
		out.push(...page);
	}
	return out.slice(0, n);
}

// BLANK stands for the blank vote in a ranking and in the duels
// (polls.BlankID): a ballot may end with it, a blank ballot ranks it alone.
export const BLANK = 0;

// Count is a tally of ballots (polls.Tally), the blank vote counted as one
// more candidate (BLANK): blank counts the ballots that rank it first.
export interface Count {
	voters: number;
	blank: number;
	first: Map<number, number>;
	ranked: Map<number, number>;
	beats: Map<number, Map<number, number>>;
}

export function count(candidates: number[], ballots: { kind: BallotKind; choices: number[] }[]): Count {
	const ids = [...new Set([...candidates, BLANK])];
	const c: Count = { voters: 0, blank: 0, first: new Map(), ranked: new Map(), beats: new Map() };
	for (const id of ids) {
		c.first.set(id, 0);
		c.ranked.set(id, 0);
		c.beats.set(id, new Map(ids.filter((x) => x !== id).map((x) => [x, 0])));
	}
	const known = new Set(ids);
	for (const b of ballots) {
		if (b.kind !== 'choices' && b.kind !== 'blank') continue;
		const choices = b.kind === 'blank' ? [BLANK] : b.choices;
		const ranked = [...new Set(choices.filter((id) => known.has(id)))];
		if (ranked.length === 0) continue;
		c.voters++;
		if (ranked[0] === BLANK) c.blank++;
		c.first.set(ranked[0], c.first.get(ranked[0])! + 1);
		const seen = new Set(ranked);
		ranked.forEach((a, k) => {
			c.ranked.set(a, c.ranked.get(a)! + 1);
			const row = c.beats.get(a)!;
			for (const x of ranked.slice(k + 1)) row.set(x, row.get(x)! + 1);
			for (const x of ids) if (!seen.has(x)) row.set(x, row.get(x)! + 1);
		});
	}
	return c;
}

// schulze returns, per candidate, how many others it beats in the Schulze
// order, and the Condorcet winner if there is one (polls.Schulze).
export function schulze(ids: number[], beats: Map<number, Map<number, number>>): { wins: Map<number, number>; winner: number | null } {
	const n = ids.length;
	const d = (i: number, j: number) => beats.get(ids[i])?.get(ids[j]) ?? 0;
	const p = ids.map((_, i) => ids.map((_, j) => (i !== j && d(i, j) > d(j, i) ? d(i, j) : 0)));
	for (let k = 0; k < n; k++)
		for (let i = 0; i < n; i++) {
			if (i === k) continue;
			for (let j = 0; j < n; j++) if (j !== i && j !== k) p[i][j] = Math.max(p[i][j], Math.min(p[i][k], p[k][j]));
		}
	const wins = new Map<number, number>();
	let winner: number | null = null;
	for (let i = 0; i < n; i++) {
		let w = 0;
		let duels = 0;
		for (let j = 0; j < n; j++) {
			if (i === j) continue;
			if (p[i][j] > p[j][i]) w++;
			if (d(i, j) > d(j, i)) duels++;
		}
		wins.set(ids[i], w);
		if (n > 1 && duels === n - 1) winner = ids[i];
	}
	return { wins, winner };
}

function sameTally(c: Count, t: TallyEntry): boolean {
	if (c.voters !== t.voters || c.blank !== t.blank || c.ranked.get(BLANK) !== (t.blank_ranked ?? 0)) return false;
	for (const [id, v] of c.first) if (id !== BLANK && (t.first[id] ?? 0) !== v) return false;
	for (const [id, v] of c.ranked) if (id !== BLANK && (t.ranked[id] ?? 0) !== v) return false;
	for (const [a, row] of c.beats)
		for (const [b, v] of row) {
			const logged = a === BLANK ? t.blank_beats?.[b] : t.beats[a]?.[b];
			if ((logged ?? 0) !== v) return false;
		}
	return true;
}

// sameShown compares the count with the results on the page: the voters,
// the blank votes, each candidate's first choices and rankings, the
// preference ranks (the blank vote's too) and the Condorcet winner.
function sameShown(c: Count, shown: PollResults): boolean {
	if (c.voters !== shown.voters) return false;
	if (shown.hidden) return true;
	if ((shown.blank ?? 0) !== c.blank || (shown.blank_ranked ?? 0) !== c.ranked.get(BLANK)) return false;
	const ids = [...shown.candidates.map((x) => x.person_id), BLANK];
	const { wins, winner } = schulze(ids, c.beats);
	if ((shown.condorcet_winner ?? null) !== winner) return false;
	const rankOf = (id: number) => 1 + ids.filter((x) => wins.get(x)! > wins.get(id)!).length;
	if (shown.blank_rank !== rankOf(BLANK)) return false;
	for (const x of shown.candidates) {
		if (x.first_choices !== (c.first.get(x.person_id) ?? 0) || x.ranked !== (c.ranked.get(x.person_id) ?? 0)) return false;
		if (x.preference_rank !== rankOf(x.person_id)) return false;
	}
	return true;
}

// recount runs the steps one after the other and reports each as it goes.
export async function recount(eventId: number, shown: PollResults, report: (s: Step) => void): Promise<boolean> {
	const w = shown.window;
	report({ id: 'signed', state: 'running' });
	const head = await checkHead();
	if ('fault' in head) {
		report({ id: 'signed', state: 'failed', detail: head.fault.detail });
		return false;
	}
	const tree: Tree = head.tree;
	const counted = shown.log_size;
	if (tree.size < counted) {
		report({ id: 'signed', state: 'failed', detail: `the results count ${counted} entries, the signed log holds ${tree.size}` });
		return false;
	}
	report({ id: 'signed', state: 'ok', n: tree.size });

	report({ id: 'tree', state: 'running' });
	const log = await entries(tree.size);
	const hash = treeHash(log.map((e) => leafHash(leafData(e.seq, e.kind, e.body))));
	if (log.length !== tree.size || !equal(hash, tree.hash)) {
		report({ id: 'tree', state: 'failed', n: log.length });
		return false;
	}
	report({ id: 'tree', state: 'ok', n: log.length });

	// The roots the window's tree had, and the window's ballots, in log
	// order; the last tally of the window.
	report({ id: 'proofs', state: 'running', n: 0 });
	const roots = new Set<string>();
	const last = new Map<string, LogBallot>();
	let tally: TallyEntry | null = null;
	let proved = 0;
	const scope = digest(scopeName(eventId, w)).toString();
	for (const e of log.slice(0, counted)) {
		let body: Record<string, unknown>;
		try {
			body = JSON.parse(e.body);
		} catch {
			continue;
		}
		if (body.series !== w.series || body.window !== w.window) continue;
		if (e.kind === 'root') roots.add(String(body.root));
		if (body.poll !== eventId) continue;
		if (e.kind === 'tally') tally = body as unknown as TallyEntry;
		if (e.kind !== 'ballot') continue;
		const b = body as unknown as LogBallot;
		const msg = digest(message(eventId, w, b.seq, { kind: b.kind, choices: b.choices ?? [] })).toString();
		let ok = b.proof.scope === scope && b.proof.message === msg && roots.has(b.proof.merkleTreeRoot);
		if (ok) {
			try {
				ok = await verifyProof(b.proof);
			} catch {
				ok = false;
			}
		}
		if (!ok) {
			report({ id: 'proofs', state: 'failed', n: proved, detail: `entry ${e.seq}` });
			return false;
		}
		proved++;
		report({ id: 'proofs', state: 'running', n: proved });
		const prev = last.get(b.proof.nullifier);
		if (!prev || b.seq > prev.seq) last.set(b.proof.nullifier, b);
	}
	report({ id: 'proofs', state: 'ok', n: proved });

	report({ id: 'count', state: 'running' });
	const ballots = [...last.values()].map((b) => ({ kind: b.kind, choices: b.choices ?? [] }));
	if (tally) {
		const c = count(
			Object.keys(tally.first)
				.map(Number)
				.filter((id) => id !== BLANK),
			ballots
		);
		if (!sameTally(c, tally)) {
			report({ id: 'count', state: 'failed', n: c.voters });
			return false;
		}
		report({ id: 'count', state: 'ok', n: c.voters });
	} else {
		report({ id: 'count', state: 'skipped' });
	}

	report({ id: 'shown', state: 'running' });
	const c = count(
		shown.candidates.map((x) => x.person_id),
		ballots
	);
	if (!sameShown(c, shown)) {
		report({ id: 'shown', state: 'failed', n: c.voters });
		return false;
	}
	report({ id: 'shown', state: 'ok', n: c.voters });
	return true;
}
