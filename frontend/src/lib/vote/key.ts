// The voter secret s of tartuffe-vote/2 (SPEC §4.1, §4.2): derived from the
// voter's passkey where the platform offers the WebAuthn PRF extension,
// else random and backed up by 12 words or a device link. Whoever holds s
// votes as the voter. What this device keeps of the vote: the secret, the
// leaf, the position, the relays the key ranks, the ballots cast.

import { writable, get } from 'svelte/store';
import { entropyToMnemonic, mnemonicToEntropy, validateMnemonic } from '@scure/bip39';
import { wordlist as french } from '@scure/bip39/wordlists/french.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { FR, T_PRF, T_RELAY, fromBytes, h, hex, toBytes, leafOf } from './crypto';
import { relayRanking } from './mailbox';

export interface VoteKey {
	// slug names the event the key is for; eventId its number.
	slug: string;
	eventId: number;
	// secret is s, hex. prf: it comes from the passkey (nothing to back up).
	secret: string;
	prf: boolean;
	// leaf is H(T_ID, event, s); position its place in the tree, once
	// registered (-1 before).
	leaf: string;
	position: number;
	// registration: the box the registration went to, and its message id.
	registration?: { box: string; id: string; checkpoint?: number };
	// sticky is the box ballots go to; ranking the full order.
	ranking: string[];
	sticky: string;
	// backups: what copies the voter confirmed.
	backups: { words: boolean; passkey: boolean; linked: number };
	// seq is the last ballot's sequence number cast from this key.
	seq: number;
}

const STORAGE_KEY = 'vote2_key';

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

// prfSupported tells whether the platform may offer PRF passkeys.
export function prfSupported(): boolean {
	return typeof window !== 'undefined' && !!window.PublicKeyCredential && typeof PublicKeyCredential.isUserVerifyingPlatformAuthenticatorAvailable === 'function';
}

const RP_NAME = 'tartuffe.me';

// createPasskeySecret creates a passkey and reads its PRF output for the
// event (two prompts: creation, then an assertion that returns the PRF).
// It returns null when the platform gives no PRF.
export async function createPasskeySecret(slug: string): Promise<{ secret: bigint; credentialId: string } | null> {
	const prfInput = new TextEncoder().encode(`tartuffe-vote/2/${slug}`);
	const userId = crypto.getRandomValues(new Uint8Array(16));
	const cred = (await navigator.credentials.create({
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
	if (!cred) return null;
	const ext = cred.getClientExtensionResults() as { prf?: { enabled?: boolean; results?: { first?: ArrayBuffer } } };
	if (!ext.prf?.enabled) return null;
	const credentialId = b64url(new Uint8Array(cred.rawId));
	let first = ext.prf.results?.first;
	if (!first) {
		const got = await assertPRF(slug, credentialId);
		if (!got) return null;
		first = got.buffer as ArrayBuffer;
	}
	return { secret: secretFromPRF(new Uint8Array(first)), credentialId };
}

// assertPRF asks the passkey for the PRF output (a restore on another
// device of the same ecosystem).
export async function assertPRF(slug: string, credentialId?: string): Promise<Uint8Array | null> {
	const prfInput = new TextEncoder().encode(`tartuffe-vote/2/${slug}`);
	const cred = (await navigator.credentials.get({
		publicKey: {
			challenge: crypto.getRandomValues(new Uint8Array(32)),
			rpId: window.location.hostname,
			userVerification: 'required',
			allowCredentials: credentialId ? [{ type: 'public-key', id: b64urlDecode(credentialId).buffer as ArrayBuffer }] : [],
			extensions: { prf: { eval: { first: prfInput } } } as AuthenticationExtensionsClientInputs
		}
	})) as PublicKeyCredential | null;
	if (!cred) return null;
	const ext = cred.getClientExtensionResults() as { prf?: { results?: { first?: ArrayBuffer } } };
	const first = ext.prf?.results?.first;
	return first ? new Uint8Array(first) : null;
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

export function secretFromWords(words: string): bigint {
	const w = words.trim().toLowerCase().split(/\s+/).join(' ');
	if (!validateMnemonic(w, french)) throw new Error('not 12 French words');
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
