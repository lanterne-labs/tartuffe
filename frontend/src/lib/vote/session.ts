// The voter's session: everything the vote page shows comes from here.
// It reads the mirror and the mailbox, never our server; it keeps the key
// (key.ts), warms the prover (prove.ts), keeps the path (tree.ts), casts
// ballots and follows their acknowledgement and receipt (protocol.ts).

import { writable, get, type Writable } from 'svelte/store';
import { bb, big, hex, nullifierOf, type Ranking } from './crypto';
import { voteKey, newVoteKey, randomSecret, createPasskeySecret, prfSupported, secretFromWords, newWords, type VoteKey, type LastBallot } from './key';
import { NostrMailbox, linkTag, type Mailbox, type NostrEvent } from './mailbox';
import { defaultSources, type Sources } from './sources';
import { warmUp, type Prover } from './prove';
import { ensureIsolated } from './isolate';
import { newReceiver, linkURL, seal, open as openLink, type LinkCode } from './link';
import { findPosition, updatePath, type PathState } from './tree';
import { parseCheckpoint, postWithAck, makeBallot, registerBody, registrationRequest, findReceipt, myPosition, complaintBody, type Setup, type Ack, type CheckpointInfo } from './protocol';
import { openCheckpoint } from './checkpoint';

export type Phase = 'loading' | 'no-key' | 'registering' | 'joining' | 'ready' | 'closed' | 'error';

export interface BallotStatus {
	// missing: acknowledged for a checkpoint that does not list it; the
	// complaint is posted (SPEC §7.2).
	state: 'idle' | 'proving' | 'sending' | 'sent' | 'received' | 'verified' | 'rejected' | 'failed' | 'missing';
	ranking?: Ranking;
	piHash?: string;
	postedAt?: number;
	checkpoint?: number;
	reason?: string;
	logSeq?: number;
	// What a complaint needs: the body as posted, its id and the signed ack.
	body?: string;
	nostrId?: string;
	ackEvent?: NostrEvent;
	complaints?: string[];
}

export interface Tally {
	seq: number;
	at: string;
	tally: { upto: number; c: number };
	results?: {
		voters: number;
		options: { slot: number; kind: string; name?: string; first: number; ranked: number; rank: number; wins: number }[];
		condorcet_winner?: number;
	};
}

export interface Stats {
	ballots: number;
	since_tally: number;
	registrations: number;
}

const ACK_TIMEOUT = 20_000;
const REGISTER_ACK_TIMEOUT = 60_000;

export class VoteSession {
	readonly phase: Writable<Phase> = writable('loading');
	readonly error: Writable<string> = writable('');
	readonly setup: Writable<Setup | null> = writable(null);
	readonly checkpoint: Writable<CheckpointInfo | null> = writable(null);
	readonly tally: Writable<Tally | null> = writable(null);
	readonly stats: Writable<Stats | null> = writable(null);
	readonly ballot: Writable<BallotStatus> = writable({ state: 'idle' });
	readonly warm: Writable<'idle' | 'artifact' | 'backend' | 'ready' | 'failed'> = writable('idle');
	readonly isolated: Writable<boolean> = writable(false);

	private mail: Mailbox | null = null;
	private prover: Prover | null = null;
	private path: PathState | null = null;
	private stopCheckpoints: (() => void) | null = null;
	private logKey = '';

	constructor(
		readonly slug: string,
		private readonly src: Sources = defaultSources()
	) {}

	// init loads the setup and the latest checkpoint, then decides the phase.
	async init(): Promise<void> {
		try {
			await bb();
			const setup = await this.src.json<Setup>('setup.json');
			if (!setup) throw new Error('no setup on the mirror');
			this.setup.set(setup);
			this.logKey = (import.meta.env.VITE_VOTE_LOG_KEY as string | undefined) || (await this.src.text('log-key.txt'))?.trim() || '';
			const stats = await this.src.json<Stats>('stats.json');
			if (stats) this.stats.set(stats);
			await this.refreshCheckpoint();
			this.mail = new NostrMailbox(
				setup.setup.relays.filter((r) => r.state === 'active').map((r) => r.url),
				`tartuffe-vote/2/${this.slug}`,
				setup.setup.nostr_pubkey
			);
			this.watchCheckpoints();
			const k = get(voteKey);
			if (Date.now() >= Date.parse(setup.setup.closes)) this.phase.set('closed');
			else if (!k || k.slug !== this.slug) this.phase.set('no-key');
			else if (k.position < 0) {
				this.phase.set('joining');
				void this.finishJoin(k);
			} else {
				this.phase.set('ready');
				this.resumeLastBallot(k);
				void this.prepare();
			}
		} catch (e) {
			this.error.set(e instanceof Error ? e.message : String(e));
			this.phase.set('error');
		}
	}

	// resumeLastBallot shows where the last ballot of this device stands
	// and, when it was acknowledged but not yet found, finishes the check
	// against the checkpoints of this visit (SPEC §7.2: a later visit).
	private resumeLastBallot(k: VoteKey) {
		const lb = k.lastBallot;
		if (!lb) return;
		this.ballot.set({ state: lb.state, piHash: lb.piHash, postedAt: lb.postedAt, checkpoint: lb.checkpoint, nostrId: lb.nostrId, body: lb.body, ackEvent: lb.ackEvent, logSeq: lb.logSeq, complaints: lb.complaints });
		const cp = get(this.checkpoint);
		if (lb.state === 'received' && cp) void this.afterCheckpoint(cp);
	}

	// rememberBallot keeps the ballot's check with the key.
	private rememberBallot(b: BallotStatus) {
		if (!b.piHash || !b.postedAt || !b.checkpoint || !b.nostrId || !b.body || !b.ackEvent) return;
		if (b.state !== 'received' && b.state !== 'verified' && b.state !== 'missing') return;
		const lb: LastBallot = { state: b.state, piHash: b.piHash, postedAt: b.postedAt, checkpoint: b.checkpoint, nostrId: b.nostrId, body: b.body, ackEvent: b.ackEvent, logSeq: b.logSeq, complaints: b.complaints };
		voteKey.update((x) => (x ? { ...x, lastBallot: lb } : x));
	}

	private async refreshCheckpoint(): Promise<CheckpointInfo | null> {
		const note = await this.src.text('checkpoint.txt');
		if (!note) return null;
		return this.takeCheckpoint(note);
	}

	private takeCheckpoint(note: string): CheckpointInfo | null {
		if (this.logKey && !openCheckpoint(note, this.logKey)) {
			console.warn('a checkpoint that does not verify');
			return null;
		}
		const info = parseCheckpoint(note);
		if (!info) return null;
		const cur = get(this.checkpoint);
		if (cur && info.index <= cur.index) return cur;
		this.checkpoint.set(info);
		void this.refreshPublic(info);
		return info;
	}

	// watchCheckpoints follows the server's checkpoints on the key's sticky
	// box (or the first box), the same subscription for every visitor; a
	// box that cannot be reached gives way to the next of the ranking, and
	// the mirror's checkpoint is read meanwhile.
	private watchCheckpoints(skip = 0) {
		const k = get(voteKey);
		const order = k ? [k.sticky, ...k.ranking.filter((b) => b !== k.sticky)] : (this.mail?.boxes() ?? []);
		const box = order[skip % Math.max(order.length, 1)];
		if (!box || !this.mail) return;
		this.stopCheckpoints?.();
		this.stopCheckpoints = this.mail.serverMessages(
			box,
			'checkpoint',
			Math.floor(Date.now() / 1000) - 600,
			(m) => {
				const info = this.takeCheckpoint(m.body);
				if (info) void this.afterCheckpoint(info);
			},
			() => {
				console.warn('vote: checkpoint relay unreachable, moving on', box);
				void this.refreshCheckpoint().then((info) => info && this.afterCheckpoint(info));
				this.watchCheckpoints(skip + 1);
			}
		);
	}

	private async refreshPublic(cp: CheckpointInfo) {
		const [tally, stats] = await Promise.all([this.src.json<Tally>('tallies/latest.json', cp.mirror), this.src.json<Stats>('stats.json', cp.mirror)]);
		if (tally) this.tally.set(tally);
		if (stats) this.stats.set(stats);
	}

	// afterCheckpoint: a ballot waiting for its receipt, a registration
	// waiting for its position.
	private async afterCheckpoint(cp: CheckpointInfo) {
		const b = get(this.ballot);
		if ((b.state === 'received' || b.state === 'sent') && b.piHash && b.postedAt && b.checkpoint && cp.index >= b.checkpoint) {
			const entry = await findReceipt(this.src, cp.mirror, b.piHash, new Date(b.postedAt));
			if (entry) {
				this.ballot.update((x) => ({ ...x, state: 'verified', logSeq: entry.log_seq }));
				this.rememberBallot(get(this.ballot));
			}
			// Not listed by the checkpoint after the named one: the mirror
			// had time; the server's own ack is now the proof.
			else if (cp.index > b.checkpoint && get(this.ballot).state === 'received') await this.complain(b);
		}
		const k = get(voteKey);
		if (k && k.position < 0 && k.registration?.checkpoint && cp.index >= k.registration.checkpoint) await this.finishJoin(k);
		if (k && k.position >= 0 && this.path) void this.refreshPath(cp);
	}

	// --- Registration ---

	// newKey makes a key: with a passkey where possible, else random (the
	// 12 words must then be confirmed before the first ballot).
	async newKey(prefer: 'passkey' | 'words'): Promise<{ key: VoteKey; words?: string }> {
		const setup = get(this.setup);
		if (!setup) throw new Error('not ready');
		const boxes = setup.setup.relays.filter((r) => r.state === 'active').map((r) => r.url);
		if (prefer === 'passkey' && prfSupported()) {
			try {
				const got = await createPasskeySecret(this.slug);
				if (got) {
					const key = newVoteKey(this.slug, setup.setup.event, got.secret, true, boxes);
					return { key };
				}
			} catch (e) {
				console.warn('passkey', e);
			}
		}
		const words = newWords();
		const key = newVoteKey(this.slug, setup.setup.event, secretFromWords(words), false, boxes);
		return { key, words };
	}

	// restoreFromWords rebuilds the key of 12 words; the position comes
	// from the positions shard.
	async restoreFromWords(words: string): Promise<void> {
		const setup = get(this.setup);
		if (!setup) throw new Error('not ready');
		const boxes = setup.setup.relays.filter((r) => r.state === 'active').map((r) => r.url);
		const key = newVoteKey(this.slug, setup.setup.event, secretFromWords(words), false, boxes);
		key.backups.words = true;
		const pos = await findPosition(this.src, key.leaf);
		if (pos === null) throw new Error('not registered');
		key.position = pos;
		const shard = await this.src.json<Record<string, number>>(`index/${key.leaf.slice(2, 4)}.json`);
		const n = hex(nullifierOf(BigInt(key.eventId), big(key.secret)));
		const idx = await this.src.json<Record<string, number>>(`index/${n.slice(2, 4)}.json`);
		key.seq = idx?.[n] ?? shard?.[n] ?? 0;
		voteKey.set(key);
		this.phase.set('ready');
		void this.prepare();
	}

	// --- The device link (SPEC §4.2) ---

	// receiveLink makes the code a new device shows and waits for the
	// secret a device that holds it posts for that code; the promise
	// resolves once the key is adopted, or rejects when stopped.
	receiveLink(votePage: string): { url: string; stop: () => void; done: Promise<void> } {
		const setup = get(this.setup);
		if (!setup || !this.mail) throw new Error('not ready');
		const boxes = setup.setup.relays.filter((r) => r.state === 'active').map((r) => r.url);
		const { code, priv } = newReceiver();
		const stops: (() => void)[] = [];
		const stopAll = () => stops.splice(0).forEach((s) => s());
		let reject: (e: Error) => void = () => {};
		let taken = false;
		const done = new Promise<void>((resolve, rej) => {
			reject = rej;
			const since = Math.floor(Date.now() / 1000) - 60;
			for (const box of boxes) {
				stops.push(
					this.mail!.links(box, code.pub, since, async (sealed) => {
						const p = openLink(priv, sealed);
						if (!p || taken) return;
						taken = true;
						stopAll();
						try {
							await this.adoptLinked(p.secret, p.position, p.seq, boxes);
							resolve();
						} catch (e) {
							rej(e instanceof Error ? e : new Error(String(e)));
						}
					})
				);
			}
		});
		return {
			url: linkURL(votePage, code),
			stop: () => {
				stopAll();
				reject(new Error('stopped'));
			},
			done
		};
	}

	// adoptLinked takes a secret another device sent: the key is rebuilt
	// and its position checked against the mirror.
	private async adoptLinked(secret: string, position: number, seq: number, boxes: string[]) {
		const setup = get(this.setup);
		if (!setup) throw new Error('not ready');
		const key = newVoteKey(this.slug, setup.setup.event, big(secret), false, boxes);
		const pos = await findPosition(this.src, key.leaf);
		if (pos === null && position < 0) throw new Error('not registered');
		key.position = pos ?? position;
		key.seq = seq;
		key.backups.linked = 1;
		voteKey.set(key);
		this.phase.set('ready');
		this.watchCheckpoints();
		void this.prepare();
	}

	// sendKey posts this device's secret, sealed for the code's receiver,
	// to the relay the code names.
	async sendKey(code: LinkCode): Promise<void> {
		const k = get(voteKey);
		const setup = get(this.setup);
		if (!k || k.position < 0 || !setup || !this.mail) throw new Error('not ready');
		const sealed = seal(code, { secret: k.secret, position: k.position, seq: k.seq });
		let lastErr = 'no relay answered';
		for (const box of [k.sticky, ...k.ranking.filter((b) => b !== k.sticky)]) {
			try {
				await this.mail.post(box, 'link', sealed, [['t', linkTag(code.pub)]]);
				voteKey.update((x) => (x ? { ...x, backups: { ...x.backups, linked: x.backups.linked + 1 } } : x));
				return;
			} catch (e) {
				lastErr = e instanceof Error ? e.message : String(e);
			}
		}
		throw new Error(lastErr);
	}

	// registrationRequest is what the ZKPassport component binds.
	registrationRequest(key: VoteKey) {
		return registrationRequest(key, this.slug);
	}

	// register posts the registration of a key with its passport proof and
	// waits for the acknowledgement; the position comes at the checkpoint.
	async register(key: VoteKey, uid: string, proof: unknown, proofDate: Date): Promise<void> {
		if (!this.mail) throw new Error('not ready');
		this.phase.set('registering');
		const body = registerBody(key, uid, proof, proofDate);
		const { posted, ack } = await postWithAck(this.mail, [key.ranking[0], ...key.ranking.slice(1)], 'register', body, REGISTER_ACK_TIMEOUT);
		if (ack.outcome === 'rejected') {
			this.phase.set('no-key');
			throw new Error(ack.reason ?? 'rejected');
		}
		key.registration = { box: posted.box, id: posted.id, checkpoint: ack.checkpoint };
		voteKey.set(key);
		this.phase.set('joining');
		this.watchCheckpoints();
		void this.prepare();
	}

	// finishJoin finds the key's position once its checkpoint is out.
	private async finishJoin(k: VoteKey) {
		const cp = get(this.checkpoint) ?? (await this.refreshCheckpoint());
		if (!cp || !k.registration?.checkpoint || cp.index < k.registration.checkpoint) return;
		const pos = await myPosition(this.src, cp.mirror, k.leaf, new Date());
		if (pos === null) return;
		voteKey.update((x) => (x ? { ...x, position: pos } : x));
		this.phase.set('ready');
		void this.prepare();
	}

	// --- Readiness ---

	// prepare does the background work before a vote: isolation, the
	// prover, the path.
	async prepare(): Promise<void> {
		this.isolated.set(await ensureIsolated());
		if (!this.prover) {
			this.warm.set('artifact');
			warmUp(this.src, (s) => this.warm.set(s))
				.then((p) => (this.prover = p))
				.catch((e) => {
					console.error('prover', e);
					this.warm.set('failed');
				});
		}
		const cp = get(this.checkpoint);
		if (cp) await this.refreshPath(cp);
	}

	private async refreshPath(cp: CheckpointInfo) {
		const k = get(voteKey);
		if (!k || k.position < 0) return;
		try {
			const state = this.path ?? { position: k.position, inner: null, path: [], root: '', treeSize: 0 };
			const next = await updatePath(this.src, cp.mirror, state, k.leaf);
			if (next.root !== cp.treeRoot) {
				console.warn('path root differs from the checkpoint', next.root, cp.treeRoot);
				return;
			}
			this.path = next;
		} catch (e) {
			console.warn('path', e);
		}
	}

	// complain reposts the ballot to the next two relays and posts the
	// signed ack as a complaint there (SPEC §7.2): the relays keep it for
	// anyone to judge against the log.
	private async complain(b: BallotStatus) {
		const k = get(voteKey);
		if (!k || !this.mail || !b.body || !b.nostrId || !b.ackEvent || !b.piHash) return;
		const next = k.ranking.slice(1).filter((box) => box !== k.sticky).slice(0, 2);
		const complaint = complaintBody(k.eventId, b.nostrId, b.piHash, b.ackEvent);
		const ids: string[] = [];
		for (const box of next) {
			try {
				await this.mail.post(box, 'ballot', b.body);
				const posted = await this.mail.post(box, 'complaint', complaint);
				ids.push(posted.id);
			} catch (e) {
				console.warn('complaint', box, e);
			}
		}
		console.warn('vote: ballot missing from checkpoint', b.checkpoint, 'complaint posted', ids);
		this.ballot.update((x) => ({ ...x, state: 'missing', complaints: ids }));
		this.rememberBallot(get(this.ballot));
	}

	// --- Casting ---

	async cast(r: Ranking): Promise<void> {
		const k = get(voteKey);
		const setup = get(this.setup);
		const cp = get(this.checkpoint);
		if (!k || k.position < 0 || !setup || !cp || !this.mail) throw new Error('not ready');
		console.warn('vote: cast', r, 'prover', !!this.prover, 'path', this.path?.root, 'cp', cp.treeRoot);
		if (!this.prover) this.prover = await warmUp(this.src, (s) => this.warm.set(s));
		if (!this.path || this.path.root !== cp.treeRoot) await this.refreshPath(cp);
		if (!this.path) throw new Error('no path');
		console.warn('vote: proving with root', this.path.root);
		for (let attempt = 0; attempt < 3; attempt++) {
			const seq = k.seq + 1;
			this.ballot.set({ state: 'proving', ranking: r });
			const { body, piHash } = await makeBallot(this.prover, k, setup, this.path.path, this.path.root, seq, r);
			this.ballot.set({ state: 'sending', ranking: r, piHash });
			const postedAt = Date.now();
			let res: { ack: Ack; ackEvent: NostrEvent; posted: { id: string } };
			try {
				res = await postWithAck(this.mail, [k.sticky, ...k.ranking.slice(1).filter((b) => b !== k.sticky)], 'ballot', body, ACK_TIMEOUT);
			} catch (e) {
				this.ballot.set({ state: 'failed', ranking: r, reason: e instanceof Error ? e.message : String(e) });
				throw e;
			}
			const ack = res.ack;
			if (ack.outcome === 'logged') {
				voteKey.update((x) => (x ? { ...x, seq } : x));
				this.ballot.set({ state: 'received', ranking: r, piHash, postedAt, checkpoint: ack.checkpoint, body, nostrId: res.posted.id, ackEvent: res.ackEvent });
				this.rememberBallot(get(this.ballot));
				return;
			}
			if (ack.reason === 'stale_seq') {
				// Another device voted since: continue from the log's seq.
				const n = hex(nullifierOf(BigInt(k.eventId), big(k.secret)));
				const idx = await this.src.json<Record<string, number>>(`index/${n.slice(2, 4)}.json`);
				const latest = idx?.[n] ?? k.seq;
				voteKey.update((x) => (x ? { ...x, seq: Math.max(latest, x.seq) } : x));
				continue;
			}
			if (ack.reason === 'old_root' || ack.reason === 'unknown_setup') {
				await this.refreshCheckpoint();
				const fresh = get(this.checkpoint);
				if (fresh) await this.refreshPath(fresh);
				continue;
			}
			this.ballot.set({ state: 'rejected', ranking: r, reason: ack.reason });
			throw new Error(ack.reason ?? 'rejected');
		}
		throw new Error('could not cast after three tries');
	}

	destroy() {
		this.stopCheckpoints?.();
	}
}
