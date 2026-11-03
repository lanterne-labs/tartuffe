// The vote (isoloir, docs: the @isoloir/web README) as the pages see it:
// one Session per event, built from the app's settings, its state as
// Svelte stores. Every protocol rule is the library's; this file only
// says where things are (the ledger's origin, the pinned log key, the
// registry, the poll of each event, the mirror) and how the app proves
// (a Web Worker, the circuit read from the mirror).
//
// Settings, read at build time (VITE_*):
//   VITE_VOTE_ORIGIN       the ledger's origin, e.g. tartuffe.me/isoloir
//   VITE_VOTE_LOG_KEY      the log's C2SP verifier key, pinned in the build
//   VITE_VOTE_REGISTRY     the registry id (default 1)
//   VITE_VOTE_POLLS        the poll of each event: slug:id, comma-separated
//   VITE_VOTE_MIRROR       the mirror as a directory URL (a static host)
//   VITE_VOTE_MIRROR_REPO  the mirror as a GitHub repository (owner/repo), read by commit through jsDelivr

import { writable, get, type Writable } from 'svelte/store';
import { browser } from '$app/environment';
import {
	Session,
	Mirror,
	Core,
	loadWasm,
	WorkerProver,
	dirSource,
	jsDelivrSource,
	IsoloirError,
	toIsoloirError,
	type IsoloirWasm,
	type MirrorSource,
	type SessionState,
	type LinkReceiver,
	type LinkSender,
	type PollSetup,
	type TallyVerdict,
	type StoredKey,
	type WorkerLike,
	type ProverOptions
} from '@isoloir/web';
import ProveWorker from '@isoloir/web/prove.worker?worker';
import { bech32, hex } from '@scure/base';
import { ensureIsolated } from './isolate';

// The prover's large public files, fetched whole from jsDelivr and used only
// if their SHA-256 is the one pinned here, so this site's host serves none
// of them. The SRS is the head of Aztec's (lanterne-labs/vote-srs, at one
// commit: every ballot circuit fits in 2^17 points); the wasm is bb.js's
// own build, from npm at the version the library runs.
const SRS = 'https://cdn.jsdelivr.net/gh/lanterne-labs/vote-srs@5d97f5383bc827f0b3cda77ffb2d96168ccc2899';
const PROVER_FILES: ProverOptions = {
	srs: {
		g1: { url: `${SRS}/g1_compressed.dat`, sha256: 'fad8203ae1fd103658d4bb33b2b4cf126a28352b5de4684737cff23cc15fb94b' },
		g2: { url: `${SRS}/g2.dat`, sha256: '01797bfc4de5a96f0e516a9ea4537d18786dc30cb991aca4274c95822b69c32f' }
	},
	wasm: {
		threads: {
			url: 'https://cdn.jsdelivr.net/npm/@aztec/bb.js@5.0.0/dest/node/barretenberg_wasm/barretenberg-threads.wasm.gz',
			sha256: '2da2861cee60c2af5a87621474517e56b5d833db8a765f075e6cbc5509308eaa'
		}
	}
};

const env = import.meta.env;

export const VOTE_ORIGIN = ((env.VITE_VOTE_ORIGIN as string | undefined) ?? '').trim();
export const LOG_KEY = ((env.VITE_VOTE_LOG_KEY as string | undefined) ?? '').trim();
export const REGISTRY = Number((env.VITE_VOTE_REGISTRY as string | undefined) || '1');

// parsePolls reads "slug:id,slug:id".
export function parsePolls(s: string): Record<string, number> {
	const out: Record<string, number> = {};
	for (const part of s.split(',')) {
		const [slug, id] = part.split(':').map((x) => x.trim());
		if (slug && id && /^\d+$/.test(id)) out[slug] = Number(id);
	}
	return out;
}

export const POLLS = parsePolls((env.VITE_VOTE_POLLS as string | undefined) ?? '');

export function pollOf(slug: string): number | null {
	return POLLS[slug] ?? null;
}

// The mirror's sources, in the order they are tried: the directory, then
// the repository (read by commit first when a checkpoint names one).
export function mirrorSources(): MirrorSource[] {
	const list: MirrorSource[] = [];
	const dir = ((env.VITE_VOTE_MIRROR as string | undefined) ?? '').trim();
	if (dir) list.push(dirSource(dir));
	const repo = ((env.VITE_VOTE_MIRROR_REPO as string | undefined) ?? '').trim();
	if (repo) list.push(jsDelivrSource(repo));
	return list;
}

// settingsProblem names what the build lacks for the vote of an event.
export function settingsProblem(slug: string): string | null {
	const missing: string[] = [];
	if (!VOTE_ORIGIN) missing.push('VITE_VOTE_ORIGIN');
	if (!LOG_KEY) missing.push('VITE_VOTE_LOG_KEY');
	if (!Number.isInteger(REGISTRY) || REGISTRY < 0) missing.push('VITE_VOTE_REGISTRY');
	if (pollOf(slug) === null) missing.push(`VITE_VOTE_POLLS (${slug})`);
	if (mirrorSources().length === 0) missing.push('VITE_VOTE_MIRROR or VITE_VOTE_MIRROR_REPO');
	return missing.length ? `the build has no ${missing.join(', ')}` : null;
}

let wasm: Promise<IsoloirWasm> | null = null;

// The wasm core, loaded once (the .wasm file is served next to the module).
export function wasmModule(): Promise<IsoloirWasm> {
	if (!wasm) {
		wasm = loadWasm();
		wasm.catch(() => (wasm = null));
	}
	return wasm;
}

export async function core(): Promise<Core> {
	return new Core(await wasmModule());
}

function keyName(): string {
	return `isoloir/${VOTE_ORIGIN}/${REGISTRY}`;
}

// The key this browser keeps for the ledger and registry, as stored by
// the session (no secret leaves this module: the pages see what they show).
export interface KeyInfo {
	source: StoredKey['source'];
	backups: StoredKey['backups'];
	registered: boolean;
	hasWords: boolean;
	// The last choice of each poll this device knows, by poll id.
	choices: Record<string, number[] | null>;
}

function readKey(): KeyInfo | null {
	if (!browser || !VOTE_ORIGIN) return null;
	try {
		const raw = localStorage.getItem(`${keyName()}/key`);
		if (!raw) return null;
		const k = JSON.parse(raw) as StoredKey;
		const choices: KeyInfo['choices'] = {};
		for (const [id, p] of Object.entries(k.polls ?? {})) {
			const c = p.ballot?.choice;
			choices[id] = c && 'rank' in c ? c.rank : null;
		}
		return { source: k.source, backups: k.backups, registered: k.position >= 0, hasWords: !!k.words, choices };
	} catch (e) {
		console.warn('vote: the stored key', e);
		return null;
	}
}

// voteKey: the key of this browser, kept in step with the sessions and
// with the other tabs.
export const voteKey: Writable<KeyInfo | null> = writable(readKey());
if (browser) {
	window.addEventListener('storage', (e) => {
		if (!e.key || e.key.startsWith(keyName())) voteKey.set(readKey());
	});
}

// forgetKey removes this browser's key (the votes it cast stay counted).
export function forgetKey(): void {
	for (const s of sessions.values()) if (s.session) s.session.forgetKey();
	try {
		for (const suffix of ['key', 'scan', 'cursor']) localStorage.removeItem(`${keyName()}/${suffix}`);
	} catch (e) {
		console.warn('vote: forgetting the key', e);
	}
	voteKey.set(null);
}

const emptyState: SessionState = {
	phase: 'loading',
	error: null,
	ledger: null,
	registry: null,
	poll: null,
	kind: null,
	pollStatus: 'none',
	checkpoint: null,
	checkpointAge: null,
	key: null,
	registration: { state: 'none' },
	ballot: { state: 'idle' },
	revote: { seq: null, next: null, previousChoice: null },
	tally: null,
	tallyUnverified: null,
	prover: 'idle',
	channels: [],
	linkAvailable: false,
	storage: 'best-effort'
};

export type Warm = 'idle' | 'loading' | 'ready' | 'failed';

// VoteSession: the session of one event, its state as stores.
export class VoteSession {
	readonly state: Writable<SessionState> = writable(emptyState);
	// The six digits of a device link that arrived, for the voter to compare.
	readonly offer: Writable<string | null> = writable(null);
	// When the latest verified tally was taken (its checkpoint's time, ms).
	readonly tallyAt: Writable<number | null> = writable(null);
	// The prover, loaded ahead of the vote.
	readonly warm: Writable<Warm> = writable('idle');
	readonly isolated: Writable<boolean> = writable(false);
	session: Session | null = null;
	private prover: WorkerProver | null = null;
	private stops: (() => void)[] = [];
	private starting: Promise<Session> | null = null;

	constructor(readonly slug: string) {}

	// init builds the session (once) and reads the mirror; a failure is in
	// the state (phase error, the code), and init may be called again.
	async init(): Promise<void> {
		this.state.set({ ...get(this.state), phase: 'loading', error: null });
		try {
			const s = await this.build();
			await s.init();
			voteKey.set(readKey());
			this.warmUp();
		} catch (e) {
			const err = toIsoloirError(e, 'mirror');
			console.error('vote: init', err);
			this.state.set({ ...get(this.state), phase: 'error', error: { code: err.code, detail: err.detail } });
			this.starting = null;
			if (this.session) {
				this.session.destroy();
				this.session = null;
			}
		}
	}

	private build(): Promise<Session> {
		if (this.starting) return this.starting;
		this.starting = (async () => {
			const problem = settingsProblem(this.slug);
			if (problem) throw new IsoloirError('no_setup', problem);
			// Isolation first: the one reload of a first visit happens before
			// any dialog, never in the middle of one.
			this.isolated.set(await ensureIsolated());
			const w = await wasmModule();
			const prover = new WorkerProver(
				() => new ProveWorker() as unknown as WorkerLike,
				async (circuit) => {
					const text = await this.session?.mirror.text(`circuits/${circuit}/artifact.json`);
					if (!text) throw new IsoloirError('mirror', `circuits/${circuit}/artifact.json is not on the mirror`);
					return text;
				},
				PROVER_FILES
			);
			this.prover = prover;
			const s = new Session({
				core: w,
				origin: VOTE_ORIGIN,
				verifierKey: LOG_KEY,
				registry: REGISTRY,
				poll: pollOf(this.slug)!,
				mirror: mirrorSources(),
				prover,
				wordsLang: 'fr',
				passkey: { rpName: 'tartuffe.me', userName: 'Clé de vote' },
				log: (level, msg, fields) => (level === 'error' ? console.error : level === 'warn' ? console.warn : console.debug)('vote:', msg, fields ?? '')
			});
			this.session = s;
			this.stops.push(
				s.subscribe((st) => {
					this.state.set(st);
					voteKey.set(readKey());
				}),
				s.on('link-offer', ({ code }) => this.offer.set(code)),
				s.on('tally', (t) => void this.dateTally(t))
			);
			return s;
		})();
		return this.starting;
	}

	private async dateTally(t: TallyVerdict) {
		const n = t.tally.checkpoint;
		if (n === undefined || !this.session) return;
		try {
			const cp = await this.session.mirror.checkpoint(n);
			if (cp) this.tallyAt.set(cp.time * 1000);
		} catch (e) {
			console.warn('vote: the tally checkpoint', e);
		}
	}

	// warmUp loads the circuit, the SRS and the key once the key is
	// registered, so that the only wait at the vote is the proof.
	warmUp(): void {
		const st = get(this.state);
		const circuit = st.poll?.circuit.name;
		if (!this.prover || !circuit || st.key?.position == null || get(this.warm) !== 'idle') return;
		this.warm.set('loading');
		this.prover.warm(circuit).then(
			() => this.warm.set('ready'),
			(e) => {
				console.error('vote: the prover', e);
				this.warm.set('failed');
			}
		);
	}

	// need gives the session once it exists (the calls of the pages).
	need(): Session {
		if (!this.session) throw new IsoloirError('not_ready');
		return this.session;
	}

	receiveLink(): LinkReceiver {
		this.offer.set(null);
		return this.need().receiveLink();
	}

	sendKeyTo(publicKey: string): Promise<LinkSender> {
		return this.need().sendKeyTo(publicKey);
	}

	pollSetup(): PollSetup | null {
		return get(this.state).poll;
	}

	destroy(): void {
		this.stops.splice(0).forEach((s) => s());
		this.session?.destroy();
		this.session = null;
		this.prover?.terminate();
		this.prover = null;
		this.starting = null;
		sessions.delete(this.slug);
	}
}

const sessions = new Map<string, VoteSession>();

// sessionFor gives the session of an event (one per page visit).
export function sessionFor(slug: string): VoteSession {
	let s = sessions.get(slug);
	if (!s) {
		s = new VoteSession(slug);
		sessions.set(slug, s);
	}
	return s;
}

// A device link: the new device's public key in the vote page's
// fragment, so that a camera app opens the page on the device that holds
// the key.
export function linkURL(votePage: string, publicKey: string): string {
	return `${votePage}#link=${publicKey}`;
}

export function parseLink(fragment: string): string | null {
	const m = /^#?link=(0[23][0-9a-f]{64})$/.exec(fragment.trim());
	return m ? m[1] : null;
}

// neventLink is a public page of a Nostr event (njump renders any event):
// an nevent code with the relays as hints.
export function neventLink(id: string, relays: string[]): string {
	const tlv: number[] = [0, 32, ...hex.decode(id)];
	for (const r of relays.slice(0, 3)) {
		const b = new TextEncoder().encode(r);
		tlv.push(1, b.length, ...b);
	}
	return 'https://njump.me/' + bech32.encode('nevent', bech32.toWords(Uint8Array.from(tlv)), 5000);
}

// The latest tally of an event, read without a session (the landing
// page): the poll's setup and the tally, each proven in the signed log
// under the pinned key, the tally verified by the wasm core.
export interface LatestTally {
	poll: PollSetup;
	verdict: TallyVerdict;
	at: number | null;
}

export async function latestTally(slug: string): Promise<LatestTally | null> {
	const problem = settingsProblem(slug);
	if (problem) throw new IsoloirError('no_setup', problem);
	const c = await core();
	const mirror = new Mirror(c, mirrorSources(), LOG_KEY);
	const id = pollOf(slug)!;
	const poll = await mirror.setup<PollSetup>(`polls/${id}.json`, 'poll');
	if (!poll) return null;
	const t = await mirror.latestTally(id);
	if (!t) return null;
	const verdict = c.verifyTally(poll.body, t.body);
	const n = verdict.tally.checkpoint ?? t.checkpoint;
	const cp = await mirror.checkpoint(n).catch(() => null);
	return { poll: poll.body, verdict, at: cp ? cp.time * 1000 : null };
}

