// The voter secret s of tartuffe-vote/2 (SPEC §4.1, §4.2): derived from the
// voter's passkey where the platform offers the WebAuthn PRF extension,
// else random and backed up by 12 words or a device link. Whoever holds s
// votes as the voter. What this device keeps of the vote: the secret, the
// leaf, the position, the relays the key ranks, the registration it
// posted, the ballots cast.

import { writable, get } from 'svelte/store';
import { entropyToMnemonic, mnemonicToEntropy, validateMnemonic } from '@scure/bip39';
import { wordlist as french } from '@scure/bip39/wordlists/french.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { FR, T_PRF, T_RELAY, fromBytes, h, hex, toBytes, leafOf } from './crypto';
import { relayRanking, type NostrEvent } from './mailbox';
import { VoteError } from './errors';

export interface VoteKey {
	// slug names the event the key is for; eventId its number.
	slug: string;
	eventId: number;
	// secret is s, hex. prf: it comes from the passkey (nothing to back up).
	secret: string;
	prf: boolean;
	// words: the 12 words the secret came from, kept so the voter can see
	// them again (a key from a passkey has none).
	words?: string;
	// leaf is H(T_ID, event, s); position its place in the tree, once
	// registered (-1 before).
	leaf: string;
	position: number;
	// registration: the request as posted, kept before the first post so
	// that a missed acknowledgement does not lose it (SPEC §4): the box it
	// went to, the message id and the signed message to send again, the
	// body, the proof's date (the proof is valid for the setup's validity),
	// when a box first took it, and the checkpoint the acknowledgement
	// named once it came.
	registration?: Registration;
	// regSecret is the registration key (secp256k1 secret, hex) the
	// passport proof binds (SPEC §4): kept for a new scan of the same key.
	regSecret?: string;
	// sticky is the box ballots go to; ranking the full order.
	ranking: string[];
	sticky: string;
	// backups: what copies the voter confirmed.
	backups: { words: boolean; passkey: boolean; linked: number };
	// seq is the last ballot's sequence number cast from this key.
	seq: number;
	// slots: the ranking of the last ballot this key cast, as this device
	// knows it (the ballot shown on the page).
	slots?: number[];
	// lastBallot is the last ballot this device cast and where its check
	// stands, so a later visit finishes the check (SPEC §7.2): received
	// (acknowledged, not yet found in the log), verified, or missing (the
	// complaint posted).
	lastBallot?: LastBallot;
}

export interface Registration {
	box: string;
	id: string;
	raw?: unknown;
	body?: string;
	proofDate?: number;
	sentAt?: number;
	checkpoint?: number;
}

export interface LastBallot {
	state: 'received' | 'verified' | 'missing';
	piHash: string;
	postedAt: number;
	checkpoint: number;
	nostrId: string;
	// body and ackEvent: what a complaint needs; they stay on the device
	// that posted the ballot (a linked device shows the check only).
	body?: string;
	ackEvent?: NostrEvent;
	logSeq?: number;
	// late: found in the log, but under a later checkpoint than the ack
	// named (the ack is kept: an auditor judges it).
	late?: boolean;
	complaints?: string[];
}

// Scan is a passport scan made before the key exists (SPEC §4): the
// registration key the proof binds, then the proof once the app answered,
// kept until the key is made and the registration posted.
export interface Scan {
	slug: string;
	eventId: number;
	regSecret: string;
	uid?: string;
	proof?: unknown;
	proofDate?: number;
}

const STORAGE_KEY = 'vote2_key';
const SCAN_KEY = 'vote2_scan';

function readStoredScan(): Scan | null {
	if (typeof window === 'undefined') return null;
	try {
		const raw = window.localStorage.getItem(SCAN_KEY);
		return raw ? (JSON.parse(raw) as Scan) : null;
	} catch {
		return null;
	}
}

export const pendingScan = writable<Scan | null>(readStoredScan());

if (typeof window !== 'undefined') {
	pendingScan.subscribe((s) => {
		try {
			if (s) window.localStorage.setItem(SCAN_KEY, JSON.stringify(s));
			else window.localStorage.removeItem(SCAN_KEY);
		} catch {}
	});
}

function readStored(): VoteKey | null {
	if (typeof window === 'undefined') return null;
	try {
		const raw = window.localStorage.getItem(STORAGE_KEY);
		return raw ? (JSON.parse(raw) as VoteKey) : null;
	} catch {
		return null;
	}
}

export const voteKey = writable<VoteKey | null>(readStored());

if (typeof window !== 'undefined') {
	voteKey.subscribe((k) => {
		try {
			if (k) window.localStorage.setItem(STORAGE_KEY, JSON.stringify(k));
			else window.localStorage.removeItem(STORAGE_KEY);
		} catch {}
	});
	window.addEventListener('storage', (e) => {
		if (e.key !== STORAGE_KEY) return;
		const next = readStored();
		if (JSON.stringify(next) !== JSON.stringify(get(voteKey))) voteKey.set(next);
	});
	navigator.storage?.persist?.().catch(() => {});
}

// --- Making the secret ---

// secretFromPRF derives s from a passkey's PRF output (32 bytes).
export function secretFromPRF(prf: Uint8Array): bigint {
	const lo = fromBytes(prf.slice(0, 16));
	const hi = fromBytes(prf.slice(16, 32));
	return h(T_PRF, lo, hi);
}

// randomSecret draws s (requires bb loaded for nothing; pure randomness).
export function randomSecret(): bigint {
	const b = new Uint8Array(32);
	crypto.getRandomValues(b);
	const x = fromBytes(b) % FR;
	return x === 0n ? 1n : x;
}

// prfSupported tells whether the platform may offer PRF passkeys (the
// extension itself shows only once a passkey is made or asserted).
export function prfSupported(): boolean {
	return typeof window !== 'undefined' && !!window.PublicKeyCredential && typeof PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable === 'function';
}

// prfCapability asks the browser whether it knows it supports the PRF
// extension: true, false, or null when it cannot say (older browsers).
export async function prfCapability(): Promise<boolean | null> {
	if (!prfSupported()) return false;
	const pkc = PublicKeyCredential as unknown as { getClientCapabilities?: () => Promise<Record<string, boolean>> };
	if (typeof pkc.getClientCapabilities !== 'function') return null;
	try {
		const caps = await pkc.getClientCapabilities();
		return typeof caps['extension:prf'] === 'boolean' ? caps['extension:prf'] : null;
	} catch {
		return null;
	}
}

const RP_NAME = 'tartuffe.me';

// passkeyError names what a WebAuthn call threw: the voter cancelled, the
// platform has no passkeys, or something else.
function passkeyError(e: unknown): VoteError {
	const name = e && typeof e === 'object' && 'name' in e ? String((e as { name: unknown }).name) : '';
	if (name === 'NotAllowedError' || name === 'AbortError') return new VoteError('passkey_cancelled');
	if (name === 'NotSupportedError' || name === 'SecurityError') return new VoteError('passkey_unsupported', name);
	return new VoteError('passkey_failed', e instanceof Error ? e.message : String(e));
}

// createPasskeySecret creates a passkey and reads its PRF output for the
// event (two prompts: creation, then an assertion that returns the PRF).
// It throws passkey_noprf when the platform gives no PRF (the passkey
// made is then of no use: the words are the way).
export async function createPasskeySecret(slug: string): Promise<{ secret: bigint; credentialId: string }> {
	const prfInput = new TextEncoder().encode(`tartuffe-vote/2/${slug}`);
	const userId = crypto.getRandomValues(new Uint8Array(16));
	let cred: PublicKeyCredential | null;
	try {
		cred = (await navigator.credentials.create({
			publicKey: {
				challenge: crypto.getRandomValues(new Uint8Array(32)),
				rp: { name: RP_NAME, id: window.location.hostname },
				user: { id: userId, name: 'vote', displayName: 'Clé de vote' },
				pubKeyCredParams: [
					{ type: 'public-key', alg: -7 },
					{ type: 'public-key', alg: -257 }
				],
				authenticatorSelection: { residentKey: 'required', userVerification: 'required' },
				extensions: { prf: { eval: { first: prfInput } } } as AuthenticationExtensionsClientInputs
			}
		})) as PublicKeyCredential | null;
	} catch (e) {
		throw passkeyError(e);
	}
	if (!cred) throw new VoteError('passkey_cancelled');
	const ext = cred.getClientExtensionResults() as { prf?: { enabled?: boolean; results?: { first?: ArrayBuffer } } };
	if (!ext.prf?.enabled) throw new VoteError('passkey_noprf');
	const credentialId = b64url(new Uint8Array(cred.rawId));
	let first = ext.prf.results?.first;
	if (!first) {
		const got = await assertPRF(slug, credentialId);
		first = got.buffer as ArrayBuffer;
	}
	return { secret: secretFromPRF(new Uint8Array(first)), credentialId };
}

// assertPRF asks the passkey for the PRF output: with no credential id,
// any passkey of this site the platform holds (a restore on another
// device of the same ecosystem, SPEC §4.1). It throws passkey_cancelled
// when the voter gives up and passkey_noprf when the passkey gives none.
export async function assertPRF(slug: string, credentialId?: string): Promise<Uint8Array> {
	const prfInput = new TextEncoder().encode(`tartuffe-vote/2/${slug}`);
	let cred: PublicKeyCredential | null;
	try {
		cred = (await navigator.credentials.get({
			publicKey: {
				challenge: crypto.getRandomValues(new Uint8Array(32)),
				rpId: window.location.hostname,
				userVerification: 'required',
				allowCredentials: credentialId ? [{ type: 'public-key', id: b64urlDecode(credentialId).buffer as ArrayBuffer }] : [],
				extensions: { prf: { eval: { first: prfInput } } } as AuthenticationExtensionsClientInputs
			}
		})) as PublicKeyCredential | null;
	} catch (e) {
		throw passkeyError(e);
	}
	if (!cred) throw new VoteError('passkey_cancelled');
	const ext = cred.getClientExtensionResults() as { prf?: { results?: { first?: ArrayBuffer } } };
	const first = ext.prf?.results?.first;
	if (!first) throw new VoteError('passkey_noprf');
	return new Uint8Array(first);
}

function b64url(b: Uint8Array): string {
	return btoa(String.fromCharCode(...b)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}
function b64urlDecode(s: string): Uint8Array {
	const bin = atob(s.replace(/-/g, '+').replace(/_/g, '/'));
	return Uint8Array.from(bin, (c) => c.charCodeAt(0));
}

// --- The 12 words ---

// recoveryWords encodes the secret's 16 bytes of entropy... the secret is a
// field element (31 bytes): the words carry its SHA-256-derived 16 bytes
// only when the secret itself was made from 16 bytes. So a random secret
// is made from 16 bytes of entropy expanded by hashing (secretFromWords).
export function newWords(): string {
	const entropy = crypto.getRandomValues(new Uint8Array(16));
	return entropyToMnemonic(entropy, french);
}

// normalizeWords lowercases and joins the words with single spaces.
export function normalizeWords(words: string): string {
	return words.trim().toLowerCase().split(/\s+/).join(' ');
}

export function secretFromWords(words: string): bigint {
	const w = normalizeWords(words);
	if (!validateMnemonic(w, french)) throw new VoteError('bad_words');
	const entropy = mnemonicToEntropy(w, french);
	const x = fromBytes(sha256(new Uint8Array([...new TextEncoder().encode('tartuffe-vote/2/words'), ...entropy]))) % FR;
	return x === 0n ? 1n : x;
}

// --- The key's relays ---

export function rankRelays(secret: bigint, boxes: string[]): string[] {
	const scores = (box: string) => h(T_RELAY, secret, fromBytes(sha256(new TextEncoder().encode(box))) % FR);
	return relayRanking(scores, boxes);
}

// newVoteKey assembles a key for an event from a secret (bb loaded).
export function newVoteKey(slug: string, eventId: number, secret: bigint, prf: boolean, boxes: string[]): VoteKey {
	const ranking = rankRelays(secret, boxes);
	return {
		slug,
		eventId,
		secret: hex(secret),
		prf,
		leaf: hex(leafOf(BigInt(eventId), secret)),
		position: -1,
		ranking,
		sticky: ranking[1] ?? ranking[0],
		backups: { words: false, passkey: prf, linked: 0 },
		seq: 0
	};
}

// stickyBox returns the box ballots go to: the first non-retired box after
// the registration box; on failure the next.
export function nextSticky(k: VoteKey, failed: string, retired: string[]): string {
	const order = k.ranking.slice(1).filter((b) => !retired.includes(b));
	const i = order.indexOf(failed);
	return order[i + 1] ?? order[order.length - 1] ?? k.sticky;
}

export { hex as fieldHex, toBytes as fieldBytes };
