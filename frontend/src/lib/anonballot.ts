// Anonymous ballots (backend/polls). The browser proves with the voting key
// (voter.ts) that one of the window's voters cast the ballot; the server
// never learns which. A ballot counts for a 30-day window; two series of
// windows run, so each change is cast in both open windows. Published
// ballots come out in hourly batches: the ballot shown here is the one this
// device cast, or, on another device, found among the published ones by its
// nullifier, which only the key computes. No request carries the account.
// A ballot goes out through public Nostr relays (nostr.ts), so our server
// never sees where it comes from; the browser then checks that the signed
// log holds it, as cast (votelog.ts).

import { Group } from '@semaphore-protocol/group';
import { generateProof, type SemaphoreProof } from '@semaphore-protocol/proof';
import { poseidon2 } from 'poseidon-lite';
import { keccak_256 } from '@noble/hashes/sha3.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { API_BASE } from './config';
import { ballotEvent, publish } from './nostr';
import { checkEntry, checkHead, type LogFault } from './votelog';
import { identity, type VoterKey, type WindowInfo } from './voter';

export type BallotKind = 'choices' | 'blank' | 'withdraw';

// MyBallot is a ballot: for choices, the ranked candidates, from the
// preferred one.
export interface MyBallot {
	kind: BallotKind;
	choices: number[];
}

// TREE_DEPTH is the depth every proof uses: the circuit files served are
// those of depth 20 (a million voters).
const TREE_DEPTH = 20;

// digest is the value a text becomes as message or scope: the first 31
// bytes of its SHA-256 (backend/polls/semaphore.Digest).
export function digest(text: string): bigint {
	const h = sha256(new TextEncoder().encode(text));
	let n = 0n;
	for (const b of h.slice(0, 31)) n = (n << 8n) | BigInt(b);
	return n;
}

// semaphoreHash is Semaphore's hash of the scope: keccak256 on 32 bytes,
// shifted right by 8 bits.
function semaphoreHash(v: bigint): bigint {
	const b = new Uint8Array(32);
	for (let i = 31, x = v; i >= 0; i--, x >>= 8n) b[i] = Number(x & 0xffn);
	let n = 0n;
	for (const x of keccak_256(b)) n = (n << 8n) | BigInt(x);
	return n >> 8n;
}

export function scopeName(eventId: number, w: { series: number; window: number }): string {
	return `tartuffe/ballot/v1/${eventId}/${w.series}/${w.window}`;
}

// nullifier is the voter's nullifier in the window: the circuit's
// Poseidon(hash(scope), secret).
export function nullifier(secret: string, eventId: number, w: { series: number; window: number }): string {
	return poseidon2([semaphoreHash(digest(scopeName(eventId, w))), identity(secret).secretScalar]).toString();
}

// message is the text the proof binds (backend/polls.AnonBallot.Message).
export function message(eventId: number, w: { series: number; window: number }, seq: number, b: MyBallot): string {
	const body = b.kind === 'choices' ? `choices=${b.choices.join(',')}` : b.kind;
	return `tartuffe-ballot/v1;poll=${eventId};series=${w.series};window=${w.window};seq=${seq};${body}`;
}

// --- What this device remembers ---

interface Stored {
	ballot: MyBallot;
	// seq and cast: per window ("series/window"), the last seq used.
	seq: Record<string, number>;
	// waiting: the windows whose last ballot is not published yet.
	waiting: Record<string, boolean>;
	publishedBy?: string;
	// sent: per window, the Nostr event of the last ballot and the relays
	// that took it (none: posted to our server).
	sent?: Record<string, { event: string; relays: string[] }>;
}

// Via is how a ballot goes out: the relays (none: posted to our server),
// and when the server publishes a ballot sent now at the latest.
export interface Via {
	relays: string[];
	publishedBy: string;
}

// LATE is how long after its deadline a ballot not yet published is called
// missing.
const LATE = 30 * 60_000;

// Check is what the browser found of the voter's ballot in the public log.
export type Check = { state: 'included' } | { state: 'waiting' } | { state: 'fault'; fault: LogFault };

const storeKey = (eventId: number) => `ballot:${eventId}`;
const wkey = (w: { series: number; window: number }) => `${w.series}/${w.window}`;

function readMine(eventId: number): Stored | null {
	try {
		const raw = window.localStorage.getItem(storeKey(eventId));
		return raw ? (JSON.parse(raw) as Stored) : null;
	} catch {
		return null;
	}
}

function writeMine(eventId: number, s: Stored) {
	try {
		window.localStorage.setItem(storeKey(eventId), JSON.stringify(s));
	} catch {}
	channel?.postMessage(eventId);
}

const channel = typeof BroadcastChannel !== 'undefined' ? new BroadcastChannel('anon-ballot') : null;

// onBallotChanged calls back when another tab cast a ballot.
export function onBallotChanged(callback: () => void): () => void {
	const f = () => callback();
	channel?.addEventListener('message', f);
	return () => channel?.removeEventListener('message', f);
}

// --- The API, without the account ---

async function errorBody(res: Response): Promise<{ error: string; code?: string; last_seq?: number }> {
	try {
		return await res.json();
	} catch {
		return { error: `HTTP ${res.status}` };
	}
}

function plain(path: string, init: RequestInit = {}): Promise<Response> {
	return fetch(`${API_BASE}${path}`, { ...init, credentials: 'omit', headers: { 'Content-Type': 'application/json' } });
}

interface PublicBallot {
	nullifier: string;
	seq: number;
	kind: BallotKind;
	choices: number[];
	log_seq: number;
}

const pollPath = (slug: string) => `/api/polls/${encodeURIComponent(slug)}`;

// Every voter downloads every commitment, so the request says nothing of
// which one is theirs. Cached a minute per window.
const trees = new Map<string, { at: number; leaves: string[] }>();

async function treeOf(w: WindowInfo): Promise<string[]> {
	const k = wkey(w);
	const c = trees.get(k);
	if (c && Date.now() - c.at < 60_000) return c.leaves;
	const res = await plain(`/api/voters/trees/${w.series}/${w.window}`);
	if (!res.ok) throw new Error((await errorBody(res)).error);
	const t = (await res.json()) as { leaves: string[] };
	trees.set(k, { at: Date.now(), leaves: t.leaves });
	return t.leaves;
}

// The circuit files (5.7 MB), served by the site itself: the browser caches
// them after the first ballot.
const ARTIFACTS = { wasm: '/semaphore/semaphore-20.wasm', zkey: '/semaphore/semaphore-20.zkey' };

export class BallotError extends Error {
	constructor(
		message: string,
		readonly code: string
	) {
		super(message);
	}
}

// NotInWindow: the key is not a voter of the window (a replaced key waits
// for the next one).
export class NotInWindow extends BallotError {
	constructor() {
		super('this voting key votes from the next window', 'not_in_window');
	}
}

interface Cast {
	seq: number;
	publishedBy: string;
	sent?: { event: string; relays: string[] };
}

async function castIn(slug: string, eventId: number, key: VoterKey, w: WindowInfo, seq: number, b: MyBallot, via: Via): Promise<Cast> {
	const leaves = await treeOf(w);
	if (!leaves.includes(key.commitment)) {
		trees.delete(wkey(w));
		if (!(await treeOf(w)).includes(key.commitment)) throw new NotInWindow();
	}
	const group = new Group((trees.get(wkey(w))?.leaves ?? leaves).map(BigInt));
	for (let attempt = 0; attempt < 2; attempt++) {
		const proof: SemaphoreProof = await generateProof(identity(key.secret), group, digest(message(eventId, w, seq, b)), digest(scopeName(eventId, w)), TREE_DEPTH, ARTIFACTS);
		const body = JSON.stringify({ poll: eventId, series: w.series, window: w.window, seq, kind: b.kind, choices: b.choices, proof });
		if (via.relays.length > 0) {
			const ev = ballotEvent(body);
			const took = await publish(via.relays, ev);
			if (took.length > 0) return { seq, publishedBy: via.publishedBy, sent: { event: ev.id, relays: took } };
			// No relay took it: our server takes it directly, and sees where
			// it comes from.
			console.warn('no Nostr relay took the ballot: sent to the server');
		}
		const res = await plain(`${pollPath(slug)}/ballots`, { method: 'POST', body });
		if (res.ok) return { seq, publishedBy: ((await res.json()) as { published_by: string }).published_by };
		const err = await errorBody(res);
		// Another device of the voter cast after this one's last ballot.
		if (err.code === 'stale_seq' && err.last_seq !== undefined && attempt === 0) {
			seq = err.last_seq + 1;
			continue;
		}
		throw new BallotError(err.error, err.code ?? 'error');
	}
	throw new BallotError('ballot refused', 'error');
}

// cast casts the ballot in every open window the key votes in. It returns
// when the batches will have published it at the latest.
export async function cast(slug: string, eventId: number, key: VoterKey, windows: WindowInfo[], b: MyBallot, via: Via): Promise<string> {
	const mine = readMine(eventId) ?? { ballot: b, seq: {}, waiting: {} };
	mine.sent ??= {};
	let publishedBy = '';
	let castSome = false;
	let lastErr: unknown = null;
	for (const w of windows) {
		try {
			const done = await castIn(slug, eventId, key, w, (mine.seq[wkey(w)] ?? 0) + 1, b, via);
			mine.seq[wkey(w)] = done.seq;
			mine.waiting[wkey(w)] = true;
			if (done.sent) mine.sent[wkey(w)] = done.sent;
			else delete mine.sent[wkey(w)];
			publishedBy = done.publishedBy;
			castSome = true;
		} catch (err) {
			if (!(err instanceof NotInWindow)) lastErr = err;
		}
	}
	if (!castSome) throw lastErr ?? new NotInWindow();
	mine.ballot = b;
	mine.publishedBy = publishedBy;
	writeMine(eventId, mine);
	return publishedBy;
}

// myBallot is the voter's ballot: the one this device remembers, or else
// the one published under their nullifier (cast from another device). It
// also re-casts silently in an open window that lacks it, so the ballot
// keeps counting from one window to the next. null: no ballot.
export async function myBallot(slug: string, eventId: number, key: VoterKey, windows: WindowInfo[], via: Via): Promise<{ ballot: MyBallot | null; publishedBy?: string; check?: Check }> {
	const mine = readMine(eventId);
	const found: Record<string, PublicBallot | undefined> = {};
	for (const w of windows) {
		const res = await plain(`${pollPath(slug)}/ballots/${w.series}/${w.window}`);
		if (!res.ok) continue;
		const list = (await res.json()) as PublicBallot[];
		const n = nullifier(key.secret, eventId, w);
		found[wkey(w)] = list.find((p) => p.nullifier === n);
	}
	let state: Stored | null = mine;
	// Published ballots newer than what this device knows: another device.
	for (const w of [...windows].sort((a, b) => b.start.localeCompare(a.start))) {
		const p = found[wkey(w)];
		if (!p) continue;
		if (!state || p.seq > (state.seq[wkey(w)] ?? 0)) {
			state = { ballot: { kind: p.kind, choices: p.choices ?? [] }, seq: { ...(state?.seq ?? {}), [wkey(w)]: p.seq }, waiting: {}, sent: state?.sent };
		}
	}
	if (!state) return { ballot: null };
	for (const w of windows) {
		const p = found[wkey(w)];
		if (p && p.seq >= (state.seq[wkey(w)] ?? 0)) state.waiting[wkey(w)] = false;
	}
	writeMine(eventId, state);
	// Silent refresh: a window that has no ballot of ours gets the current one.
	if (state.ballot.kind !== 'withdraw') {
		const missing = windows.filter((w) => !found[wkey(w)] && !state.waiting[wkey(w)]);
		if (missing.length > 0) {
			cast(slug, eventId, key, missing, state.ballot, via).catch((err) => {
				if (!(err instanceof NotInWindow)) console.warn('refresh of the ballot in a new window failed', err);
			});
		}
	}
	const waiting = Object.values(state.waiting).some(Boolean);
	let check: Check | undefined;
	try {
		check = await checkLog(eventId, key, windows, state, found);
	} catch (err) {
		console.warn('could not check the public log', err);
	}
	return { ballot: state.ballot.kind === 'withdraw' ? null : state.ballot, publishedBy: waiting ? state.publishedBy : undefined, check };
}

// checkLog checks the log's signed head against the last one this device
// saw, then that it holds the voter's last published ballot as cast; a
// ballot still not published long after its deadline is missing.
async function checkLog(eventId: number, key: VoterKey, windows: WindowInfo[], state: Stored, found: Record<string, PublicBallot | undefined>): Promise<Check> {
	const head = await checkHead();
	if ('fault' in head) return { state: 'fault', fault: head.fault };
	for (const w of windows) {
		const k = wkey(w);
		if (state.waiting[k]) {
			if (state.publishedBy && Date.now() > new Date(state.publishedBy).getTime() + LATE) {
				const sent = state.sent?.[k];
				return {
					state: 'fault',
					fault: {
						kind: 'missing',
						detail: `the ballot of window ${k} was due by ${state.publishedBy} and is not in the log`,
						evidence: `${head.note}\nballot event ${sent?.event ?? '(posted to the server)'} on ${sent?.relays.join(' ') ?? '-'}`
					}
				};
			}
			continue;
		}
		const p = found[k];
		if (!p || !p.log_seq || p.seq !== state.seq[k]) continue;
		const n = nullifier(key.secret, eventId, w);
		const want = state.ballot;
		const fault = await checkEntry(head.tree, head.note, p.log_seq, (b) => {
			const proof = b.proof as { nullifier?: string } | undefined;
			return (
				b.poll === eventId &&
				b.series === w.series &&
				b.window === w.window &&
				b.seq === p.seq &&
				b.kind === want.kind &&
				JSON.stringify(b.choices ?? []) === JSON.stringify(want.choices) &&
				proof?.nullifier === n
			);
		});
		if (fault) return { state: 'fault', fault };
	}
	return Object.values(state.waiting).some(Boolean) ? { state: 'waiting' } : { state: 'included' };
}

// forgetBallots drops what this device remembers of its ballots (the key
// is gone from it).
export function forgetBallots() {
	try {
		for (const k of Object.keys(window.localStorage)) if (k.startsWith('ballot:')) window.localStorage.removeItem(k);
	} catch {}
}

// rememberedBallot is the ballot this device last knew for the event,
// without asking the server (null: none, or withdrawn).
export function rememberedBallot(eventId: number): MyBallot | null {
	const s = readMine(eventId);
	return s && s.ballot.kind !== 'withdraw' ? s.ballot : null;
}
