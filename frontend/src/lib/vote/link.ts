// The device link (SPEC §4.2): the voter's secret goes from a device that
// holds it to a new one, end-to-end encrypted, through a relay. The new
// device shows a code (a URL, as a QR code and as text) carrying an
// ephemeral public key and listens on every relay of the setup; the old
// device opens the URL, the voter confirms, and it posts the secret
// encrypted for that key to the first relay that takes it. Nothing goes
// through our server; the relay sees a ciphertext.

import { secp256k1 } from '@noble/curves/secp256k1.js';
import { xchacha20poly1305 } from '@noble/ciphers/chacha.js';
import { sha256 } from '@noble/hashes/sha2.js';
import { bytesToHex, hexToBytes, randomBytes } from '@noble/hashes/utils.js';

export interface LinkCode {
	// pub is the receiver's ephemeral public key (compressed, hex).
	pub: string;
}

export interface LinkPayload {
	secret: string;
	position: number;
	seq: number;
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

// seal encrypts the payload for the receiver with a fresh sender key:
// sender public key (33) || nonce (24) || ciphertext, base64.
export function seal(code: LinkCode, payload: LinkPayload): string {
	const priv = secp256k1.utils.randomSecretKey();
	const pub = secp256k1.getPublicKey(priv, true);
	const key = sharedKey(priv, hexToBytes(code.pub));
	const nonce = randomBytes(24);
	const ct = xchacha20poly1305(key, nonce).encrypt(new TextEncoder().encode(JSON.stringify(payload)));
	const out = new Uint8Array(pub.length + nonce.length + ct.length);
	out.set(pub, 0);
	out.set(nonce, pub.length);
	out.set(ct, pub.length + nonce.length);
	return btoa(String.fromCharCode(...out));
}

// open decrypts a sealed payload with the receiver's key; null when it is
// not for this key.
export function open(priv: Uint8Array, sealed: string): LinkPayload | null {
	try {
		const raw = Uint8Array.from(atob(sealed), (c) => c.charCodeAt(0));
		const pub = raw.slice(0, 33);
		const nonce = raw.slice(33, 57);
		const ct = raw.slice(57);
		const key = sharedKey(priv, pub);
		const plain = xchacha20poly1305(key, nonce).decrypt(ct);
		const p = JSON.parse(new TextDecoder().decode(plain)) as LinkPayload;
		if (typeof p.secret !== 'string' || typeof p.position !== 'number' || typeof p.seq !== 'number') return null;
		return p;
	} catch {
		return null;
	}
}
