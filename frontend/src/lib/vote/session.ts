// The voter's session: everything the vote page shows comes from here.
// It reads the mirror and the mailbox, never our server; it keeps the key
// (key.ts), warms the prover (prove.ts), keeps the path (tree.ts), casts
// ballots and follows their acknowledgement and receipt (protocol.ts).
//
// One rule above the others: a voter is never stuck. Every wait is a
// named state with a timer behind it (the checkpoints arrive on a relay,
// and the mirror is read again every 30 s while anything is pending);
// every failure is a code the page translates, with what to do next.

import { writable, get, type Writable } from 'svelte/store';
import { bb, big, hex, nullifierOf, type Ranking } from './crypto';
import { voteKey, newVoteKey, createPasskeySecret, prfSupported, assertPRF, secretFromPRF, secretFromWords, normalizeWords, newWords, type VoteKey, type LastBallot } from './key';
import { NostrMailbox, linkTag, noteServerTime, serverNow, sinceRecent, type Mailbox, type NostrEvent, type Prepared } from './mailbox';
import { defaultSources, type Sources } from './sources';
import { warmUp, type Prover } from './prove';
import { ensureIsolated } from './isolate';
import { newReceiver, linkURL, seal, open as openLink, type LinkCode, type LinkPayload } from './link';
import { findPosition, updatePath, type PathState } from './tree';
import { parseCheckpoint, postWithAck, makeBallot, registerBody, registrationRequest, findReceipt, myPosition, complaintBody, chooseAnchor, type Setup, type Ack, type CheckpointInfo, type PostProgress } from './protocol';
import { openCheckpoint } from './checkpoint';
import { verifyTally, type TallyBody } from './tally';
import { VoteError, toVoteError } from './errors';

export type Phase = 'loading' | 'no-key' | 'registering' | 'joining' | 'ready' | 'closed' | 'error';

// KeptBallot is a proven ballot no relay acknowledged: kept to be sent
// again without proving it again (SPEC §7.1).
export interface KeptBallot {
	body: string;
	piHash: string;
	seq: number;
	root: string;
	ranking: Ranking;
}

export interface BallotStatus {
	// missing: acknowledged for a checkpoint that does not list it; the
	// complaint is posted (SPEC §7.2).
	state: 'idle' | 'proving' | 'sending' | 'sent' | 'received' | 'verified' | 'rejected' | 'failed' | 'missing';
	ranking?: Ranking;
	piHash?: string;
	postedAt?: number;
	checkpoint?: number;
	// reason: an acknowledgement's reason (rejected) or an error code
	// (failed); detail the raw text behind it.
	reason?: string;
	detail?: string;
	logSeq?: number;
	late?: boolean;
	// What a complaint needs: the body as posted, its id and the signed ack.
	body?: string;
	nostrId?: string;
	ackEvent?: NostrEvent;
	complaints?: string[];
	kept?: KeptBallot;
}

// RegistrationStatus is where the registration of this device's key
// stands: idle (a key, nothing posted yet), posting (sent, waiting for the
// acknowledgement on a box), acked (acknowledged; the position comes with
// the checkpoint), failed (what and whether the proof is still usable).
export interface RegistrationStatus {
	state: 'idle' | 'posting' | 'acked' | 'failed';
	progress?: PostProgress;
	code?: string;
	detail?: string;
	// stale: the proof is too old to send again; the passport must be
	// scanned again (with the same key).
	stale?: boolean;
}

export interface Tally {
	seq: number;
	at: string;
	tally: TallyBody;
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

// LinkReceiver is what a new device shows and waits on (SPEC §4.2): the
// URL to open on the device that holds the key; the code of a key that
// arrived, for the voter to confirm against the one shown there; the
// answer; the promise that resolves once a key is adopted.
export interface LinkReceiver {
	url: string;
	offer: Writable<string | null>;
	answer: (ok: boolean) => void;
	stop: () => void;
	done: Promise<void>;
}

const ACK_TIMEOUT = 20_000;
const REGISTER_ACK_TIMEOUT = 60_000;
const POLL_PERIOD = 30_000;
const LINK_TIMEOUT = 15 * 60_000;
const DEFAULT_R_MIN = 32;
const DEFAULT_ANCHOR_AGE = 86_400;
const KEPT_CHECKPOINTS = 64;

export class VoteSession {
	readonly phase: Writable<Phase> = writable('loading');
	// error: the code of what stopped the page (describeError gives the
	// sentence); errorDetail the raw text.
	readonly error: Writable<string> = writable('');
	readonly errorDetail: Writable<string> = writable('');
	readonly setup: Writable<Setup | null> = writable(null);
	readonly checkpoint: Writable<CheckpointInfo | null> = writable(null);
	readonly tally: Writable<Tally | null> = writable(null);
	// tallyVerified: whether the latest tally's decryption proofs hold
	// (null before any tally).
	readonly tallyVerified: Writable<boolean | null> = writable(null);
	readonly stats: Writable<Stats | null> = writable(null);
	readonly ballot: Writable<BallotStatus> = writable({ state: 'idle' });
	readonly registration: Writable<RegistrationStatus> = writable({ state: 'idle' });
	readonly warm: Writable<'idle' | 'artifact' | 'backend' | 'ready' | 'failed'> = writable('idle');
	readonly isolated: Writable<boolean> = writable(false);

	private mail: Mailbox | null = null;
	private prover: Prover | null = null;
	private path: PathState | null = null;
	private stopCheckpoints: (() => void) | null = null;
	private logKey = '';
	// checkpoints: the ones seen this visit, newest first (the anchors a
	// ballot may prove against, SPEC §5).
	private checkpoints: CheckpointInfo[] = [];
	private timer: ReturnType<typeof setInterval> | null = null;
	private polling = false;
	// seqFloor: the highest sequence number the log is known to hold for
	// this key (learned from a stale_seq rejection).
	private seqFloor = 0;

	constructor(
		readonly slug: string,
		private readonly src: Sources = defaultSources()
	) {}

	// init loads the setup and the latest checkpoint, then decides the phase.
	async init(): Promise<void> {
		this.phase.set('loading');
		this.error.set('');
		this.errorDetail.set('');
		try {
			// Isolation first (SPEC §6.5): the one reload of the first visit
			// happens before any dialog, never in the middle of one.
			this.isolated.set(await ensureIsolated());
			await bb();
			const setup = await this.src.json<Setup>('setup.json');
			if (!setup) throw new VoteError('no_setup');
			this.setup.set(setup);
			this.logKey = (import.meta.env.VITE_VOTE_LOG_KEY as string | undefined) || (await this.src.text('log-key.txt'))?.trim() || '';
			const stats = await this.src.json<Stats>('stats.json');
			if (stats) this.stats.set(stats);
			await this.refreshCheckpoint();
			this.mail = new NostrMailbox(this.boxes(), `tartuffe-vote/2/${this.slug}`, setup.setup.nostr_pubkey);
			this.watchCheckpoints();
			this.startPolling();
			const k = this.key();
			if (Date.now() >= Date.parse(setup.setup.closes)) this.phase.set('closed');
			else if (!k) this.phase.set('no-key');
			else if (k.position < 0) {
				this.phase.set('joining');
				// A request posted and never acknowledged: finishJoin looks for
				// the position first, then names the next step.
				this.registration.set({ state: k.registration?.checkpoint !== undefined ? 'acked' : 'idle' });
				await this.finishJoin(k);
			} else {
				this.phase.set('ready');
				this.registration.set({ state: 'acked' });
				this.resumeLastBallot(k);
				void this.prepare();
			}
		} catch (e) {
			const ve = toVoteError(e);
			console.error('vote: init', ve);
			this.error.set(ve.code);
			this.errorDetail.set(ve.detail);
			this.phase.set('error');
		}
	}

	// key gives this device's key for this event, or null.
	private key(): VoteKey | null {
		const k = get(voteKey);
		return k && k.slug === this.slug ? k : null;
	}

	private boxes(): string[] {
		return (get(this.setup)?.setup.relays ?? []).filter((r) => r.state === 'active').map((r) => r.url);
	}

	private requireSetup(): Setup {
		const setup = get(this.setup);
		if (!setup || !this.mail) throw new VoteError('not_ready');
		return setup;
	}

	// resumeLastBallot shows where the last ballot of this device stands
	// and, when it was acknowledged but not yet found, finishes the check
	// against the checkpoints of this visit (SPEC §7.2: a later visit).
	private resumeLastBallot(k: VoteKey) {
		const lb = k.lastBallot;
		if (!lb) return;
		const ranking: Ranking | undefined = k.slots ? { len: k.slots.length, slots: [k.slots[0] ?? 0, k.slots[1] ?? 0, k.slots[2] ?? 0] } : undefined;
		this.ballot.set({ state: lb.state, ranking, piHash: lb.piHash, postedAt: lb.postedAt, checkpoint: lb.checkpoint, nostrId: lb.nostrId, body: lb.body, ackEvent: lb.ackEvent, logSeq: lb.logSeq, late: lb.late, complaints: lb.complaints });
		const cp = get(this.checkpoint);
		if (lb.state !== 'verified' && cp) void this.afterCheckpoint(cp);
	}

	// rememberBallot keeps the ballot's check with the key.
	private rememberBallot(b: BallotStatus) {
		if (!b.piHash || !b.postedAt || !b.checkpoint || !b.nostrId) return;
		if (b.state !== 'received' && b.state !== 'verified' && b.state !== 'missing') return;
		const lb: LastBallot = { state: b.state, piHash: b.piHash, postedAt: b.postedAt, checkpoint: b.checkpoint, nostrId: b.nostrId, body: b.body, ackEvent: b.ackEvent, logSeq: b.logSeq, late: b.late, complaints: b.complaints };
		voteKey.update((x) => (x ? { ...x, lastBallot: lb } : x));
	}

	// refreshCheckpoint reads the mirror's latest checkpoint; it throws
	// log_key when the pinned key refuses it.
	private async refreshCheckpoint(): Promise<CheckpointInfo | null> {
		const note = await this.src.text('checkpoint.txt');
		if (!note) return get(this.checkpoint);
		return this.takeCheckpoint(note, true);
	}

	// takeCheckpoint checks a checkpoint note and keeps it when newer. A
	// note the log key refuses is a mismatch between the app and the log
	// (a stale pinned key, or a mirror that is not ours): the page says so
	// (fromMirror) rather than wait forever; from a relay it is dropped.
	private takeCheckpoint(note: string, fromMirror = false, live = false): CheckpointInfo | null {
		if (this.logKey && !openCheckpoint(note, this.logKey)) {
			console.warn('vote: a checkpoint that does not verify against the log key');
			if (fromMirror) throw new VoteError('log_key');
			return null;
		}
		const info = parseCheckpoint(note);
		if (!info) return null;
		noteServerTime(info.time, live);
		const cur = get(this.checkpoint);
		if (!this.checkpoints.some((c) => c.index === info.index)) {
			this.checkpoints = [info, ...this.checkpoints].sort((a, b) => b.index - a.index).slice(0, KEPT_CHECKPOINTS);
		}
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
		const k = this.key();
		const order = k ? [k.sticky, ...k.ranking.filter((b) => b !== k.sticky)] : (this.mail?.boxes() ?? []);
		const box = order[skip % Math.max(order.length, 1)];
		if (!box || !this.mail) return;
		this.stopCheckpoints?.();
		this.stopCheckpoints = this.mail.serverMessages(
			box,
			'checkpoint',
			sinceRecent(600),
			(m) => {
				const info = this.takeCheckpoint(m.body, false, m.live);
				if (info) void this.afterCheckpoint(info);
			},
			() => {
				console.warn('vote: checkpoint relay unreachable, moving on', box);
				void this.poll();
				this.watchCheckpoints(skip + 1);
			}
		);
	}

	// startPolling reads the mirror every 30 s while anything is pending
	// (a registration without its position, a ballot without its receipt):
	// a relay that stays silent, or a CDN that serves the commit late,
	// never leaves the page waiting on an event that may not come.
	private startPolling() {
		if (this.timer) return;
		this.timer = setInterval(() => void this.poll(), POLL_PERIOD);
	}

	private pending(): boolean {
		const k = this.key();
		const b = get(this.ballot);
		return (!!k && k.position < 0 && !!k.registration) || b.state === 'sent' || b.state === 'received' || b.state === 'missing';
	}

	private async poll() {
		if (this.polling || !this.pending()) return;
		this.polling = true;
		try {
			let cp: CheckpointInfo | null = null;
			try {
				cp = await this.refreshCheckpoint();
			} catch (e) {
				console.warn('vote: poll', e);
			}
			cp ??= get(this.checkpoint);
			if (cp) await this.afterCheckpoint(cp);
		} finally {
			this.polling = false;
		}
	}

	private async refreshPublic(cp: CheckpointInfo) {
		try {
			const [tally, stats] = await Promise.all([this.src.json<Tally>('tallies/latest.json', cp.mirror), this.src.json<Stats>('stats.json', cp.mirror)]);
			if (stats) this.stats.set(stats);
			if (tally) {
				const setup = get(this.setup);
				const why = setup ? verifyTally(setup.setup.pk, tally.tally) : 'no setup';
				if (why) console.error('vote: the tally does not verify', tally.seq, why);
				this.tally.set(tally);
				this.tallyVerified.set(why === null);
			}
		} catch (e) {
			console.warn('vote: public files', e);
		}
	}

	// afterCheckpoint: a ballot waiting for its receipt, a registration
	// waiting for its position. Run at every checkpoint and every poll.
	private async afterCheckpoint(cp: CheckpointInfo) {
		const b = get(this.ballot);
		if ((b.state === 'received' || b.state === 'sent' || b.state === 'missing') && b.piHash && b.postedAt && b.checkpoint && cp.index >= b.checkpoint) {
			try {
				const { entry, exact } = await findReceipt(this.src, cp.mirror, b.piHash, new Date(b.postedAt));
				if (entry) {
					// Found: verified. Under a later checkpoint than the ack
					// named, the server published it late: the ack stays, an
					// auditor judges it (SPEC §12.6); no complaint of ours.
					this.ballot.update((x) => ({ ...x, state: 'verified', logSeq: entry.log_seq, late: entry.checkpoint > (x.checkpoint ?? entry.checkpoint) || undefined }));
					this.rememberBallot(get(this.ballot));
				}
				// Not listed by the checkpoint after the named one, in files
				// read at that checkpoint's commit: the mirror had time; the
				// server's own ack is now the proof. A miss in older files
				// proves nothing: the next poll reads again.
				else if (exact && cp.index > b.checkpoint && get(this.ballot).state === 'received') await this.complain(b);
			} catch (e) {
				console.warn('vote: receipt', e);
			}
		}
		const k = this.key();
		if (k && k.position < 0) await this.finishJoin(k);
		if (k && k.position >= 0 && this.path) void this.refreshPath(cp);
	}

	// --- Registration ---

	// newKey makes a key: from a passkey, or from 12 words (then confirmed
	// before the first ballot). The key is kept at once: the dialog may be
	// closed before the scan and resumed later with the same key.
	async newKey(prefer: 'passkey' | 'words'): Promise<{ key: VoteKey; words?: string }> {
		const setup = this.requireSetup();
		const boxes = this.boxes();
		if (prefer === 'passkey') {
			if (!prfSupported()) throw new VoteError('passkey_unsupported');
			const got = await createPasskeySecret(this.slug);
			const key = newVoteKey(this.slug, setup.setup.event, got.secret, true, boxes);
			voteKey.set(key);
			this.registration.set({ state: 'idle' });
			this.phase.set('joining');
			return { key };
		}
		const words = newWords();
		const key = newVoteKey(this.slug, setup.setup.event, secretFromWords(words), false, boxes);
		key.words = words;
		voteKey.set(key);
		this.registration.set({ state: 'idle' });
		this.phase.set('joining');
		return { key, words };
	}

	// confirmWords records that the voter typed the words back.
	confirmWords() {
		voteKey.update((x) => (x ? { ...x, backups: { ...x.backups, words: true } } : x));
	}

	// forgetKey removes the key from this device (the vote stays counted).
	forgetKey() {
		voteKey.set(null);
		this.ballot.set({ state: 'idle' });
		this.registration.set({ state: 'idle' });
		this.path = null;
		if (get(this.phase) !== 'closed' && get(this.phase) !== 'error') this.phase.set('no-key');
	}

	// latestCheckpoint reads the mirror's newest checkpoint (the token the
	// restores read under, SPEC §8.3), falling back to the one held.
	private async latestCheckpoint(): Promise<CheckpointInfo | null> {
		try {
			return (await this.refreshCheckpoint()) ?? get(this.checkpoint);
		} catch (e) {
			console.warn('vote: checkpoint', e);
			return get(this.checkpoint);
		}
	}

	// locatePosition finds a leaf's position on the mirror at the
	// checkpoint given: the positions shard, then the leaves of the days
	// since the registration was sent.
	private async locatePosition(leaf: string, cp: CheckpointInfo, sentAt?: number): Promise<number | null> {
		const pos = await findPosition(this.src, leaf, cp.mirror);
		if (pos !== null) return pos;
		if (sentAt) return myPosition(this.src, cp.mirror, leaf, new Date(sentAt - 3_600_000));
		return null;
	}

	// adoptRestored takes a key found again (words, passkey, link): its
	// position and its last sequence number from the mirror.
	private async adoptRestored(key: VoteKey, hint?: Partial<LinkPayload>): Promise<void> {
		const cp = await this.latestCheckpoint();
		const token = cp?.mirror ?? '';
		const pos = cp ? await this.locatePosition(key.leaf, cp) : await findPosition(this.src, key.leaf);
		const known = hint?.position ?? -1;
		if (pos === null && known < 0) throw new VoteError('not_registered');
		key.position = pos ?? known;
		const n = hex(nullifierOf(BigInt(key.eventId), big(key.secret)));
		const idx = await this.src.json<Record<string, number>>(`index/${n.slice(2, 4)}.json`, token);
		key.seq = Math.max(idx?.[n] ?? 0, hint?.seq ?? 0);
		if (hint?.slots) key.slots = hint.slots;
		if (hint?.last) key.lastBallot = { ...hint.last };
		if (hint?.words) key.words = hint.words;
		voteKey.set(key);
		this.registration.set({ state: 'acked' });
		this.phase.set('ready');
		this.ballot.set({ state: 'idle' });
		this.resumeLastBallot(key);
		this.watchCheckpoints();
		void this.prepare();
	}

	// restoreFromWords rebuilds the key of 12 words; the position comes
	// from the positions shard at the newest checkpoint.
	async restoreFromWords(words: string): Promise<void> {
		const setup = this.requireSetup();
		const key = newVoteKey(this.slug, setup.setup.event, secretFromWords(words), false, this.boxes());
		key.words = normalizeWords(words);
		key.backups.words = true;
		await this.adoptRestored(key);
	}

	// restoreFromPasskey asks the platform for any passkey of this site
	// (Face ID, a fingerprint, a synced passkey) and rebuilds the key from
	// its PRF output (SPEC §4.1).
	async restoreFromPasskey(): Promise<void> {
		const setup = this.requireSetup();
		if (!prfSupported()) throw new VoteError('passkey_unsupported');
		const prf = await assertPRF(this.slug);
		const key = newVoteKey(this.slug, setup.setup.event, secretFromPRF(prf), true, this.boxes());
		await this.adoptRestored(key);
	}

	// --- The device link (SPEC §4.2) ---

	// receiveLink makes the code a new device shows and waits for the
	// secret a device that holds it posts for that code. A key that
	// arrives is offered with its confirmation code; the voter confirms
	// it is the one the other device shows, and only then it is adopted.
	// The wait ends after 15 minutes (link_expired) or when stopped.
	receiveLink(votePage: string): LinkReceiver {
		const boxes = this.boxes();
		if (!this.mail) throw new VoteError('not_ready');
		const { code, priv } = newReceiver();
		const stops: (() => void)[] = [];
		const stopAll = () => stops.splice(0).forEach((s) => s());
		const offer = writable<string | null>(null);
		let pending: LinkPayload | null = null;
		let taken = false;
		let settle: { resolve: () => void; reject: (e: Error) => void } = { resolve: () => {}, reject: () => {} };
		const done = new Promise<void>((resolve, reject) => {
			settle = { resolve, reject };
		});
		const expiry = setTimeout(() => {
			stopAll();
			settle.reject(new VoteError('link_expired'));
		}, LINK_TIMEOUT);
		const since = sinceRecent(60);
		for (const box of boxes) {
			stops.push(
				this.mail.links(box, code.pub, since, (sealed) => {
					if (taken || pending) return;
					const got = openLink(priv, sealed);
					if (!got) return;
					pending = got.payload;
					offer.set(got.code);
				})
			);
		}
		const answer = (ok: boolean) => {
			const p = pending;
			pending = null;
			offer.set(null);
			if (!ok || !p || taken) return;
			taken = true;
			clearTimeout(expiry);
			stopAll();
			this.adoptLinked(p)
				.then(settle.resolve)
				.catch((e) => settle.reject(toVoteError(e)));
		};
		return {
			url: linkURL(votePage, code),
			offer,
			answer,
			stop: () => {
				clearTimeout(expiry);
				stopAll();
				settle.reject(new VoteError('stopped'));
			},
			done
		};
	}

	// adoptLinked takes a secret another device sent: the key is rebuilt
	// and its position checked against the mirror.
	private async adoptLinked(p: LinkPayload) {
		const setup = this.requireSetup();
		const key = newVoteKey(this.slug, setup.setup.event, big(p.secret), false, this.boxes());
		key.backups.linked = 1;
		if (p.words) key.backups.words = true;
		await this.adoptRestored(key, p);
	}

	// sendKey posts this device's secret, sealed for the code's receiver,
	// to the first relay of the key's ranking that takes it; it returns
	// the confirmation code to show the voter.
	async sendKey(code: LinkCode): Promise<string> {
		const k = this.key();
		if (!k || k.position < 0 || !this.mail) throw new VoteError('not_ready');
		const last = k.lastBallot ? { ...k.lastBallot, body: undefined, ackEvent: undefined } : undefined;
		const sealed = seal(code, { secret: k.secret, position: k.position, seq: k.seq, slots: k.slots, last, words: k.words });
		let lastErr = 'no relay answered';
		for (const box of [k.sticky, ...k.ranking.filter((b) => b !== k.sticky)]) {
			try {
				await this.mail.post(box, 'link', sealed.sealed, [['t', linkTag(code.pub)]]);
				voteKey.update((x) => (x ? { ...x, backups: { ...x.backups, linked: x.backups.linked + 1 } } : x));
				return sealed.code;
			} catch (e) {
				lastErr = e instanceof Error ? e.message : String(e);
			}
		}
		throw new VoteError('no_relay', lastErr);
	}

	// registrationRequest is what the ZKPassport component binds.
	registrationRequest(key: VoteKey) {
		return registrationRequest(key, this.slug);
	}

	// register posts the registration of a key with its passport proof and
	// waits for the acknowledgement; the position comes at the checkpoint.
	// The request is kept with the key before anything is posted: a missed
	// acknowledgement (a closed tab, a phone that suspended the page) does
	// not burn the passport, the request is sent again as it is.
	async register(key: VoteKey, uid: string, proof: unknown, proofDate: Date): Promise<void> {
		const setup = this.requireSetup();
		const mail = this.mail!;
		const body = registerBody(key, uid, proof, proofDate);
		const msg = mail.prepare('register', body);
		const first = !key.registration?.sentAt;
		key.registration = { box: key.ranking[0], id: msg.id, raw: msg.raw, body, proofDate: proofDate.getTime(), sentAt: key.registration?.sentAt };
		voteKey.set(key);
		await this.postRegistration(key, msg, first, setup);
	}

	// resendRegistration posts the kept request again (the proof is valid
	// for the setup's validity); past that, the passport must be scanned
	// again, with the same key.
	async resendRegistration(): Promise<void> {
		const setup = this.requireSetup();
		const k = this.key();
		if (!k || k.position >= 0 || !k.registration?.raw) throw new VoteError('not_ready');
		if (this.proofStale(k, setup)) {
			this.registration.set({ state: 'failed', code: 'stale_proof', stale: true });
			throw new VoteError('stale_proof');
		}
		await this.postRegistration(k, { id: k.registration.id, raw: k.registration.raw }, !k.registration.sentAt, setup);
	}

	private proofStale(k: VoteKey, setup: Setup): boolean {
		const at = k.registration?.proofDate;
		return !at || serverNow() - at > (setup.setup.validity - 300) * 1000;
	}

	private async postRegistration(key: VoteKey, msg: Prepared, first: boolean, setup: Setup) {
		const mail = this.mail!;
		this.phase.set('registering');
		this.registration.set({ state: 'posting' });
		try {
			const { ack } = await postWithAck(mail, key.ranking, msg, REGISTER_ACK_TIMEOUT, {
				resend: !first,
				onPosted: (posted) => {
					voteKey.update((x) => (x?.registration ? { ...x, registration: { ...x.registration, box: posted.box, sentAt: x.registration.sentAt ?? Date.now() } } : x));
				},
				progress: (progress) => this.registration.update((r) => ({ ...r, progress }))
			});
			if (ack.outcome === 'rejected') this.registrationRejected(ack, first);
			voteKey.update((x) => (x?.registration ? { ...x, registration: { ...x.registration, checkpoint: ack.checkpoint } } : x));
			this.registration.set({ state: 'acked' });
			this.phase.set('joining');
			this.watchCheckpoints();
			void this.prepare();
			void this.poll();
		} catch (e) {
			const ve = toVoteError(e);
			if (this.key()) {
				const stale = ve.code === 'reg:stale_date' || ve.code === 'reg:bad_proof' || ve.code === 'stale_proof' || this.proofStale(this.key()!, setup);
				this.registration.set({ state: 'failed', code: ve.code, detail: ve.detail, stale });
				this.phase.set(ve.code === 'reg:closed' ? 'closed' : 'joining');
			}
			throw ve;
		}
	}

	// registrationRejected turns a rejection into what the voter must do.
	// uid_used on the first post of a key: the passport already has a key
	// (another device, an earlier visit), this one cannot serve and is
	// dropped. Later posts of the same key: handled as success upstream.
	private registrationRejected(ack: Ack, first: boolean): never {
		const reason = ack.reason ?? 'rejected';
		if (reason === 'uid_used' && first) {
			voteKey.set(null);
			this.registration.set({ state: 'failed', code: 'reg:uid_used' });
			this.phase.set('no-key');
			throw new VoteError('reg:uid_used');
		}
		throw new VoteError(`reg:${reason}`);
	}

	// finishJoin finds the key's position once its checkpoint is out, or
	// on any visit with a key that has none: the mirror says whether the
	// registration was logged before a new scan is ever offered.
	private async finishJoin(k: VoteKey) {
		const cp = get(this.checkpoint) ?? (await this.latestCheckpoint());
		if (!cp) return;
		try {
			const pos = await this.locatePosition(k.leaf, cp, k.registration?.sentAt);
			if (pos !== null) {
				voteKey.update((x) => (x ? { ...x, position: pos } : x));
				this.registration.set({ state: 'acked' });
				this.phase.set('ready');
				void this.prepare();
				return;
			}
		} catch (e) {
			console.warn('vote: position', e);
		}
		// Posted, nothing acknowledged and nothing on the mirror: the voter
		// decides (send again while the proof is valid, else scan again).
		const r = get(this.registration);
		const setup = get(this.setup);
		if (k.registration && k.registration.checkpoint === undefined && r.state !== 'posting' && r.state !== 'failed') {
			this.registration.set({ state: 'failed', code: 'no_ack', stale: !!setup && this.proofStale(k, setup) });
		}
		// Acknowledged for a checkpoint, and the one after it is out without
		// the leaf: the server did not keep its promise (SPEC §7.2, the
		// relays hold the signed ack for the auditors). The voter sends the
		// request again rather than wait on a poll for ever.
		else if (k.registration?.checkpoint !== undefined && cp.index > k.registration.checkpoint + 1 && r.state === 'acked') {
			console.warn('vote: registration acknowledged for checkpoint', k.registration.checkpoint, 'and not listed by', cp.index);
			this.registration.set({ state: 'failed', code: 'reg_missing', stale: !!setup && this.proofStale(k, setup) });
		}
	}

	// --- Readiness ---

	// prepare does the background work before a vote: the prover, the
	// path (isolation was settled at init).
	async prepare(): Promise<void> {
		this.isolated.set(typeof window !== 'undefined' && !!window.crossOriginIsolated);
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
		const k = this.key();
		if (!k || k.position < 0) return;
		try {
			await this.pathFor(cp);
		} catch (e) {
			console.warn('path', e);
		}
	}

	// pathFor gives the key's path at a checkpoint, recomputed from the
	// mirror at that checkpoint's commit when the held one is for another
	// root; it throws when the mirror and the checkpoint disagree.
	private async pathFor(cp: CheckpointInfo): Promise<PathState> {
		const k = this.key();
		if (!k || k.position < 0) throw new VoteError('not_ready');
		if (this.path && this.path.root === cp.treeRoot && this.path.position === k.position) return this.path;
		const state = this.path && this.path.position === k.position ? this.path : { position: k.position, inner: null, path: [], root: '', treeSize: 0 };
		const next = await updatePath(this.src, cp.mirror, state, k.leaf);
		if (next.root !== cp.treeRoot) throw new VoteError('no_path', `root ${next.root} differs from the checkpoint ${cp.treeRoot}`);
		this.path = next;
		return next;
	}

	// anchorFor picks the checkpoint a ballot proves against (SPEC §5): the
	// newest, except a first ballot prefers one that at least R_MIN later
	// registrants share, when one exists. The mirror's newest checkpoint is
	// read first, so a quiet relay does not leave the choice stale.
	private async anchorFor(k: VoteKey, setup: Setup): Promise<CheckpointInfo> {
		await this.latestCheckpoint();
		const first = k.seq === 0 && this.seqFloor === 0;
		const cp = chooseAnchor(this.checkpoints, k.position, first, setup.setup.r_min ?? DEFAULT_R_MIN, setup.setup.anchor_age || DEFAULT_ANCHOR_AGE, Math.floor(serverNow() / 1000));
		if (!cp) throw new VoteError('no_path', 'no checkpoint holds my leaf');
		return cp;
	}

	// complain reposts the ballot to the next two relays and posts the
	// signed ack as a complaint there (SPEC §7.2): the relays keep it for
	// anyone to judge against the log.
	private async complain(b: BallotStatus) {
		const k = this.key();
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

	// cast proves and posts a ranking. Whatever fails, the ballot ends in a
	// named state (failed with its code, rejected with the reason) so the
	// page offers the next step; a proven ballot no relay acknowledged is
	// kept for resend.
	async cast(r: Ranking): Promise<void> {
		const k0 = this.key();
		const setup = get(this.setup);
		if (!k0 || k0.position < 0 || !setup || !this.mail) throw new VoteError('not_ready');
		let kept: KeptBallot | undefined;
		try {
			if (!this.prover) {
				this.warm.set('artifact');
				try {
					this.prover = await warmUp(this.src, (s) => this.warm.set(s));
				} catch (e) {
					this.warm.set('failed');
					throw new VoteError('prover', e instanceof Error ? e.message : String(e));
				}
			}
			for (let attempt = 0; attempt < 3; attempt++) {
				const k = this.key()!;
				this.ballot.set({ state: 'proving', ranking: r });
				const anchor = await this.anchorFor(k, setup);
				const path = await this.pathFor(anchor);
				const seq = Math.max(k.seq, this.seqFloor) + 1 + (this.seqFloor > k.seq ? attempt : 0);
				const { body, piHash } = await makeBallot(this.prover, k, setup, path.path, path.root, seq, r);
				kept = { body, piHash, seq, root: path.root, ranking: r };
				const verdict = await this.submit(kept);
				if (verdict === 'done') return;
			}
			throw new VoteError('cast_retries');
		} catch (e) {
			const ve = toVoteError(e);
			const s = get(this.ballot).state;
			if (s === 'proving' || s === 'sending' || s === 'sent') {
				this.ballot.set({ state: 'failed', ranking: r, reason: ve.code, detail: ve.detail, kept: ve.code === 'no_relay' ? kept : undefined });
			}
			throw ve;
		}
	}

	// resend posts a kept ballot again without proving it again while its
	// root is still the current checkpoint's; otherwise it proves anew.
	async resend(): Promise<void> {
		const b = get(this.ballot);
		if (!b.kept) throw new VoteError('not_ready');
		const kept = b.kept;
		await this.latestCheckpoint();
		const cp = get(this.checkpoint);
		if (cp && cp.treeRoot === kept.root) {
			try {
				if ((await this.submit(kept)) === 'done') return;
			} catch (e) {
				const ve = toVoteError(e);
				const s = get(this.ballot).state;
				if (s === 'sending' || s === 'sent') this.ballot.set({ state: 'failed', ranking: kept.ranking, reason: ve.code, detail: ve.detail, kept: ve.code === 'no_relay' ? kept : undefined });
				throw ve;
			}
		}
		await this.cast(kept.ranking);
	}

	// submit posts a proven ballot and reads its acknowledgement: done when
	// logged; retry when the log asks for fresh values (stale_seq: another
	// device voted, old_root, unknown_setup); a rejection otherwise.
	private async submit(kept: KeptBallot): Promise<'done' | 'retry'> {
		const k = this.key()!;
		const mail = this.mail!;
		const { body, piHash, seq, ranking } = kept;
		this.ballot.set({ state: 'sending', ranking, piHash });
		const postedAt = Date.now();
		const msg = mail.prepare('ballot', body);
		const res = await postWithAck(mail, [k.sticky, ...k.ranking.slice(1).filter((b) => b !== k.sticky)], msg, ACK_TIMEOUT, {
			onPosted: () => this.ballot.update((x) => ({ ...x, state: 'sent', postedAt }))
		});
		const ack = res.ack;
		if (ack.outcome === 'logged') {
			const slots = ranking.slots.slice(0, ranking.len);
			voteKey.update((x) => (x ? { ...x, seq, slots } : x));
			this.seqFloor = 0;
			this.ballot.set({ state: 'received', ranking, piHash, postedAt, checkpoint: ack.checkpoint, body, nostrId: res.posted.id, ackEvent: res.ackEvent });
			this.rememberBallot(get(this.ballot));
			void this.poll();
			return 'done';
		}
		if (ack.reason === 'stale_seq') {
			// Another device voted since: continue past the log's sequence
			// number, read at the newest checkpoint (SPEC §6.3).
			const cp = await this.latestCheckpoint();
			const n = hex(nullifierOf(BigInt(k.eventId), big(k.secret)));
			const idx = await this.src.json<Record<string, number>>(`index/${n.slice(2, 4)}.json`, cp?.mirror ?? '');
			this.seqFloor = Math.max(idx?.[n] ?? 0, seq, this.seqFloor);
			return 'retry';
		}
		if (ack.reason === 'old_root' || ack.reason === 'unknown_setup') {
			await this.latestCheckpoint();
			return 'retry';
		}
		this.ballot.set({ state: 'rejected', ranking, reason: ack.reason ?? 'rejected' });
		throw new VoteError(ack.reason ?? 'rejected');
	}

	destroy() {
		this.stopCheckpoints?.();
		if (this.timer) clearInterval(this.timer);
		this.timer = null;
	}
}
