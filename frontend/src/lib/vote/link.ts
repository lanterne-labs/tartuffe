// The device link (SPEC §4.2): the voter's secret goes from a device that
// holds it to a new one, end-to-end encrypted, through a relay. The new
// device shows a code (a URL, as a QR code and as text) carrying an
// ephemeral public key and listens on every relay of the setup; the old
// device opens the URL, the voter confirms, and it posts the secret
// encrypted for that key to the first relay that takes it. Nothing goes
// through our server; the relay sees a ciphertext.
//
// Both devices derive a 6-digit code from the shared key: the sender shows
// it, the receiver asks the voter whether it is the one shown there before
// adopting what it decrypted, so that nobody who saw the URL (or runs the
// relay) can push their own key into the new device.

import { secp256k1 } from '@noble/curves/secp256k1.js';
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex, hexToBytes, randomBytes } from '@noble/hashes/utils.js';
import type { LastBallot } from './key';

export interface LinkCode {
	// pub is the receiver's ephemeral public key (compressed, hex).
	pub: string;
}

// LinkedBallot is the last ballot's check as it travels with the key: the
// body and the signed ack stay on the device that posted them (they only
// serve a complaint there).
export type LinkedBallot = Omit<LastBallot, 'body' | 'ackEvent'>;

export interface LinkPayload {
	secret: string;
	position: number;
	seq: number;
	// slots: the ranking of the last ballot, as the sending device shows it.
	slots?: number[];
	last?: LinkedBallot;
	// words: the 12 words when the key has them (the new device can show
	// them too).
	words?: string;
}

// newReceiver makes the receiver's ephemeral key.
export function newReceiver(): { code: LinkCode; priv: Uint8Array } {
	const priv = secp256k1.utils.randomSecretKey();
	return { code: { pub: bytesToHex(secp256k1.getPublicKey(priv, true)) }, priv };
}

// The code in a URL: the vote page's fragment, so that a camera app opens
// the page on the device that holds the key.
export function linkURL(votePage: string, code: LinkCode): string {
	return `${votePage}#link=${code.pub}`;
}

export function parseLink(fragment: string): LinkCode | null {
	const m = /^#?link=([0-9a-f]{66})$/.exec(fragment.trim());
	return m ? { pub: m[1] } : null;
}

function sharedKey(priv: Uint8Array, pub: Uint8Array): Uint8Array {
	// ECDH, the x coordinate hashed with a domain tag.
	const point = secp256k1.getSharedSecret(priv, pub, true);
	return sha256(new Uint8Array([...new TextEncoder().encode('tartuffe-vote/2/link'), ...point.slice(1)]));
}

// confirmCode is the 6 digits both devices derive from the shared key.
export function confirmCode(key: Uint8Array): string {
	const d = sha256(new Uint8Array([...key, ...new TextEncoder().encode('tartuffe-vote/2/link-code')]));
	const n = ((d[0] << 24) | (d[1] << 16) | (d[2] << 8) | d[3]) >>> 0;
	return String(n % 1_000_000).padStart(6, '0');
}

// seal encrypts the payload for the receiver with a fresh sender key:
// sender public key (33) || nonce (24) || ciphertext, base64; and the code
// the sender shows.
export function seal(code: LinkCode, payload: LinkPayload): { sealed: string; code: string } {
	const priv = secp256k1.utils.randomSecretKey();
	const pub = secp256k1.getPublicKey(priv, true);
	const key = sharedKey(priv, hexToBytes(code.pub));
	const nonce = randomBytes(24);
	const ct = xchacha20poly1305(key, nonce).encrypt(new TextEncoder().encode(JSON.stringify(payload)));
	const out = new Uint8Array(pub.length + nonce.length + ct.length);
	out.set(pub, 0);
	out.set(nonce, pub.length);
	out.set(ct, pub.length + nonce.length);
	return { sealed: btoa(String.fromCharCode(...out)), code: confirmCode(key) };
}

// open decrypts a sealed payload with the receiver's key and gives the
// code to confirm; null when it is not for this key.
export function open(priv: Uint8Array, sealed: string): { payload: LinkPayload; code: string } | null {
	try {
		const raw = Uint8Array.from(atob(sealed), (c) => c.charCodeAt(0));
		const pub = raw.slice(0, 33);
		const nonce = raw.slice(33, 57);
		const ct = raw.slice(57);
		const key = sharedKey(priv, pub);
		const plain = xchacha20poly1305(key, nonce).decrypt(ct);
		const p = JSON.parse(new TextDecoder().decode(plain)) as LinkPayload;
		if (typeof p.secret !== 'string' || typeof p.position !== 'number' || typeof p.seq !== 'number') return null;
		return { payload: p, code: confirmCode(key) };
	} catch {
		return null;
	}
}
