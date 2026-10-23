// The voting key: a secret s kept on the voter's devices, never sent to the
// server in clear. Its commitment C was admitted by the voter's document
// (POST /api/voters/register/{provider}); every ballot proves, without
// saying which, that its author holds the secret of one admitted commitment
// (backend/polls/semaphore). Whoever holds s votes as the voter: the key
// gets to other devices by a passkey (an encrypted copy in the vault, opened
// by the passkey only), by a device link (QR code), or by the 12 recovery
// words. None of the requests here carries the account's session: nothing
// ties the key to an account.

import { writable, get } from 'svelte/store';
import { Identity } from '@semaphore-protocol/identity';
import { entropyToMnemonic, mnemonicToEntropy, validateMnemonic } from '@scure/bip39';
import { wordlist as french } from '@scure/bip39/wordlists/french.js';
import { hkdf } from '@noble/hashes/hkdf.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { API_BASE } from './config';

// Backups say which copies of the key the voter made.
export interface Backups {
	passkey: boolean;
	recovery: boolean;
	// linked counts the devices this one gave the key to.
	linked: number;
}

export interface VoterKey {
	// secret is the 16 random bytes, as hex.
	secret: string;
	commitment: string;
	// registered: the document admitted the commitment.
	registered: boolean;
	// waitingUntil: a replaced key votes from this date (ISO), when the
	// next windows open.
	waitingUntil?: string;
	backups: Backups;
}

const STORAGE_KEY = 'voter_key';

function readStored(): VoterKey | null {
	if (typeof window === 'undefined') return null;
	try {
		const raw = window.localStorage.getItem(STORAGE_KEY);
		return raw ? (JSON.parse(raw) as VoterKey) : null;
	} catch {
		return null;
	}
}

export const voterKey = writable<VoterKey | null>(readStored());

if (typeof window !== 'undefined') {
	voterKey.subscribe((k) => {
		try {
			if (k) window.localStorage.setItem(STORAGE_KEY, JSON.stringify(k));
			else window.localStorage.removeItem(STORAGE_KEY);
		} catch {}
	});
	window.addEventListener('storage', (e) => {
		if (e.key !== STORAGE_KEY) return;
		const next = readStored();
		if (JSON.stringify(next) !== JSON.stringify(get(voterKey))) voterKey.set(next);
	});
}

// Whether the dialog that gets a voting key (the passport scan) is open.
export const voterKeyOpen = writable(false);

export function toHex(b: Uint8Array): string {
	return Array.from(b, (x) => x.toString(16).padStart(2, '0')).join('');
}

export function fromHex(h: string): Uint8Array {
	const out = new Uint8Array(h.length / 2);
	for (let i = 0; i < out.length; i++) out[i] = parseInt(h.slice(2 * i, 2 * i + 2), 16);
	return out;
}

function b64url(b: Uint8Array): string {
	return btoa(String.fromCharCode(...b)).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function fromB64url(s: string): Uint8Array {
	const pad = s.replace(/-/g, '+').replace(/_/g, '/') + '==='.slice((s.length + 3) % 4);
	return Uint8Array.from(atob(pad), (c) => c.charCodeAt(0));
}

// identity is the Semaphore identity of a secret.
export function identity(secret: string): Identity {
	return new Identity(fromHex(secret));
}

// newKey makes a voting key, not yet registered.
export function newKey(): VoterKey {
	const secret = toHex(crypto.getRandomValues(new Uint8Array(16)));
	return { secret, commitment: identity(secret).commitment.toString(), registered: false, backups: { passkey: false, recovery: false, linked: 0 } };
}

// adoptSecret keeps a secret that came from another device (or the
// recovery words), registered already.
export function adoptSecret(secret: string, backups: Partial<Backups> = {}) {
	voterKey.set({
		secret,
		commitment: identity(secret).commitment.toString(),
		registered: true,
		backups: { passkey: false, recovery: false, linked: 0, ...backups }
	});
}

// binding is the custom data the passport proof binds: the challenge and the
// commitment (backend/accounts/providers.Binding).
export function binding(challenge: string, commitment: string): string {
	return toHex(sha256(new TextEncoder().encode(`${challenge}:${commitment}`)));
}

async function errorText(res: Response): Promise<string> {
	try {
		const body = await res.json();
		if (body && typeof body.error === 'string') return body.error;
	} catch {}
	return `HTTP ${res.status}`;
}

// plain calls the API without the account's session.
function plain(path: string, init: RequestInit = {}): Promise<Response> {
	return fetch(`${API_BASE}${path}`, {
		...init,
		credentials: 'omit',
		headers: { 'Content-Type': 'application/json', ...(init.headers ?? {}) }
	});
}

// challenge asks the one-time challenge a document proof binds.
export async function challenge(provider: string): Promise<string> {
	const res = await plain(`/api/auth/challenge/${provider}`, { method: 'POST' });
	if (!res.ok) throw new Error(await errorText(res));
	return ((await res.json()) as { challenge: string }).challenge;
}

export interface WindowInfo {
	series: number;
	window: number;
	start: string;
	end: string;
}

export interface Registration {
	status: 'registered' | 'replaced' | 'already';
	from: WindowInfo[];
}

// register sends the document's proof for the key's commitment and keeps
// the key as registered.
export async function register(provider: string, key: VoterKey, payload: Record<string, unknown>): Promise<Registration> {
	const res = await plain(`/api/voters/register/${provider}`, {
		method: 'POST',
		body: JSON.stringify({ ...payload, commitment: key.commitment })
	});
	if (!res.ok) throw new Error(await errorText(res));
	const reg = (await res.json()) as Registration;
	const waiting = reg.status === 'replaced' ? reg.from.map((w) => w.start).sort()[0] : undefined;
	voterKey.set({ ...key, registered: true, waitingUntil: waiting });
	return reg;
}

// --- Recovery words ---

// recoveryWords are the 12 French words that hold the secret.
export function recoveryWords(secret: string): string[] {
	return entropyToMnemonic(fromHex(secret), french).split(' ');
}

// secretFromWords reads the 12 words back ("" when they are not valid).
export function secretFromWords(words: string): string {
	const norm = words.trim().toLowerCase().normalize('NFKD').split(/\s+/).join(' ');
	const m = norm.normalize('NFKD');
	if (!validateMnemonic(m, french)) return '';
	return toHex(mnemonicToEntropy(m, french));
}

// --- Passkey vault ---

const PRF_SALT = new TextEncoder().encode('tartuffe-voting-key-vault-v1');

// passkeySupported tells whether the browser has passkeys at all (the PRF
// extension is known only once one is used).
export function passkeySupported(): boolean {
	return typeof window !== 'undefined' && !!window.PublicKeyCredential && !!navigator.credentials;
}

// The PRF output gives three values: where the copy is, the key that
// encrypts it, and the token that may replace it.
function vaultKeys(prf: Uint8Array) {
	const derive = (info: string) => hkdf(sha256, prf, undefined, new TextEncoder().encode(info), 32);
	return { lookup: b64url(derive('lookup')), aes: derive('aes'), write: b64url(derive('write')) };
}

function prfOutput(cred: Credential | null): Uint8Array | null {
	// eslint-disable-next-line @typescript-eslint/no-explicit-any
	const res = (cred as any)?.getClientExtensionResults?.()?.prf?.results?.first;
	return res ? new Uint8Array(res) : null;
}

async function passkeyPRF(): Promise<Uint8Array> {
	const cred = await navigator.credentials.get({
		publicKey: {
			challenge: crypto.getRandomValues(new Uint8Array(32)),
			rpId: window.location.hostname,
			userVerification: 'required',
			extensions: { prf: { eval: { first: PRF_SALT } } } as AuthenticationExtensionsClientInputs
		}
	});
	const out = prfOutput(cred);
	if (!out) throw new PasskeyUnsupported();
	return out;
}

// PasskeyUnsupported: the browser or the password manager lacks the PRF
// extension.
export class PasskeyUnsupported extends Error {
	constructor() {
		super('passkey PRF unsupported');
	}
}

async function seal(aes: Uint8Array, plain: Uint8Array): Promise<string> {
	const key = await crypto.subtle.importKey('raw', aes as BufferSource, 'AES-GCM', false, ['encrypt']);
	const iv = crypto.getRandomValues(new Uint8Array(12));
	const ct = new Uint8Array(await crypto.subtle.encrypt({ name: 'AES-GCM', iv }, key, plain as BufferSource));
	const out = new Uint8Array(iv.length + ct.length);
	out.set(iv);
	out.set(ct, iv.length);
	return b64url(out);
}

async function open(aes: Uint8Array, sealed: string): Promise<Uint8Array> {
	const raw = fromB64url(sealed);
	const key = await crypto.subtle.importKey('raw', aes as BufferSource, 'AES-GCM', false, ['decrypt']);
	return new Uint8Array(await crypto.subtle.decrypt({ name: 'AES-GCM', iv: raw.slice(0, 12) }, key, raw.slice(12) as BufferSource));
}

// protectWithPasskey creates a passkey (Face ID, fingerprint or PIN) and
// leaves in the vault a copy of the secret only it opens.
export async function protectWithPasskey(key: VoterKey): Promise<void> {
	const cred = await navigator.credentials.create({
		publicKey: {
			rp: { id: window.location.hostname, name: 'tartuffe.me' },
			user: { id: crypto.getRandomValues(new Uint8Array(16)), name: 'Clé de vote tartuffe.me', displayName: 'Clé de vote tartuffe.me' },
			challenge: crypto.getRandomValues(new Uint8Array(32)),
			pubKeyCredParams: [
				{ type: 'public-key', alg: -7 },
				{ type: 'public-key', alg: -257 }
			],
			authenticatorSelection: { residentKey: 'required', userVerification: 'required' },
			extensions: { prf: { eval: { first: PRF_SALT } } } as AuthenticationExtensionsClientInputs
		}
	});
	// Some browsers give the PRF output at creation, others only on use.
	const prf = prfOutput(cred) ?? (await passkeyPRF());
	const v = vaultKeys(prf);
	const res = await plain(`/api/vault/${v.lookup}`, {
		method: 'PUT',
		body: JSON.stringify({ ciphertext: await seal(v.aes, fromHex(key.secret)), write_token: v.write })
	});
	if (!res.ok) throw new Error(await errorText(res));
	voterKey.set({ ...key, backups: { ...key.backups, passkey: true } });
}

// restoreFromPasskey opens the copy of the secret with a passkey of this
// device (or synced to it).
export async function restoreFromPasskey(): Promise<void> {
	const v = vaultKeys(await passkeyPRF());
	const res = await plain(`/api/vault/${v.lookup}`);
	if (res.status === 404) throw new Error('no voting key under this passkey');
	if (!res.ok) throw new Error(await errorText(res));
	const { ciphertext } = (await res.json()) as { ciphertext: string };
	adoptSecret(toHex(await open(v.aes, ciphertext)), { passkey: true });
}

// --- Device links ---
//
// The new device shows a QR code: a link to /link with, in the fragment
// (never sent to a server), a channel name and its public key. The device
// that has the key opens it, confirms, and leaves at the channel the secret
// (and a session of the account, if signed in) encrypted for that public
// key. The new device takes it and is ready: one step.

export interface LinkOffer {
	url: string;
	// wait resolves with what the other device sent, or null on timeout.
	wait: (signal: AbortSignal) => Promise<LinkPayload | null>;
}

export interface LinkPayload {
	secret: string;
	session?: { token: string; expires_at: string; user: unknown };
}

const ECDH = { name: 'ECDH', namedCurve: 'P-256' } as const;

async function sharedKey(priv: CryptoKey, pubRaw: Uint8Array): Promise<Uint8Array> {
	const pub = await crypto.subtle.importKey('raw', pubRaw as BufferSource, ECDH, false, []);
	const bits = new Uint8Array(await crypto.subtle.deriveBits({ name: 'ECDH', public: pub }, priv, 256));
	return hkdf(sha256, bits, undefined, new TextEncoder().encode('tartuffe-device-link-v1'), 32);
}

// offerLink prepares this (new) device to receive the key.
export async function offerLink(): Promise<LinkOffer> {
	const pair = (await crypto.subtle.generateKey(ECDH, false, ['deriveBits'])) as CryptoKeyPair;
	const pub = new Uint8Array(await crypto.subtle.exportKey('raw', pair.publicKey));
	const channel = b64url(crypto.getRandomValues(new Uint8Array(32)));
	const url = `${window.location.origin}/link#c=${channel}&k=${b64url(pub)}`;
	const wait = async (signal: AbortSignal): Promise<LinkPayload | null> => {
		const until = Date.now() + 10 * 60 * 1000;
		while (Date.now() < until && !signal.aborted) {
			const res = await plain(`/api/links/${channel}`, { signal });
			if (res.status === 200) {
				const { ciphertext } = (await res.json()) as { ciphertext: string };
				const [epk, sealed] = ciphertext.split('.');
				const aes = await sharedKey(pair.privateKey, fromB64url(epk));
				return JSON.parse(new TextDecoder().decode(await open(aes, sealed))) as LinkPayload;
			}
			await new Promise((r) => setTimeout(r, 2000));
		}
		return null;
	};
	return { url, wait };
}

// parseLink reads a link URL (or its fragment) shown by a new device.
export function parseLink(text: string): { channel: string; pub: Uint8Array } | null {
	const hash = text.includes('#') ? text.slice(text.indexOf('#') + 1) : text;
	const p = new URLSearchParams(hash);
	const channel = p.get('c') ?? '';
	const k = p.get('k') ?? '';
	if (!/^[A-Za-z0-9_-]{32,128}$/.test(channel) || !k) return null;
	try {
		return { channel, pub: fromB64url(k) };
	} catch {
		return null;
	}
}

// answerLink sends this device's key (and the session) to the new device.
export async function answerLink(link: { channel: string; pub: Uint8Array }, payload: LinkPayload): Promise<void> {
	const pair = (await crypto.subtle.generateKey(ECDH, true, ['deriveBits'])) as CryptoKeyPair;
	const epk = new Uint8Array(await crypto.subtle.exportKey('raw', pair.publicKey));
	const aes = await sharedKey(pair.privateKey, link.pub);
	const sealed = await seal(aes, new TextEncoder().encode(JSON.stringify(payload)));
	const res = await plain(`/api/links/${link.channel}`, { method: 'POST', body: JSON.stringify({ ciphertext: `${b64url(epk)}.${sealed}` }) });
	if (!res.ok) throw new Error(await errorText(res));
	const key = get(voterKey);
	if (key) voterKey.set({ ...key, backups: { ...key.backups, linked: key.backups.linked + 1 } });
}

// protectedEnough: the key has a copy outside this browser.
export function protectedEnough(k: VoterKey | null): boolean {
	return !!k && (k.backups.passkey || k.backups.recovery || k.backups.linked > 0);
}
