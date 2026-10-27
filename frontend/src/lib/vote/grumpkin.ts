// The Grumpkin curve in the browser (backend/vote/grumpkin): y² = x³ − 17
// over the BN254 scalar field, scalars in the BN254 base field, the
// generator (1, √−16) of the Noir standard library, points compressed as
// x with the parity of y in the top bit (SPEC §6.4). Built on noble's
// generic short-Weierstrass arithmetic: the page checks the tally's
// decryption proofs with it (SPEC §12), nothing is signed here.

import { weierstrass } from '@noble/curves/abstract/weierstrass.js';
import { FR, big, hex } from './crypto';

// ORDER is the group order: the BN254 base field modulus q.
export const ORDER = 21888242871839275222246405745257275088696311157297823662689037894645226208583n;

export const Grumpkin = weierstrass({
	p: FR,
	n: ORDER,
	h: 1n,
	a: 0n,
	b: FR - 17n,
	Gx: 1n,
	Gy: 17631683881184975370165255887551781615748388533673675138860n
});

export type GPoint = InstanceType<typeof Grumpkin>;

export const G = Grumpkin.BASE;

// decompress decodes a compressed point, refusing what is not canonical: x
// at or above the field, no square root, the point at infinity.
export function decompress(s: string): GPoint | null {
	if (!/^0x[0-9a-f]{64}$/i.test(s)) return null;
	const v = big(s);
	const parity = (v >> 255n) & 1n;
	const x = v & ((1n << 255n) - 1n);
	if (x >= FR) return null;
	const rhs = (x * x * x - 17n + FR * 17n) % FR;
	let y: bigint;
	try {
		y = Grumpkin.Fp.sqrt(rhs);
	} catch {
		return null;
	}
	if ((y * y) % FR !== rhs) return null;
	if ((y & 1n) !== parity) y = FR - y;
	if (x === 0n && y === 0n) return null;
	try {
		const p = Grumpkin.fromAffine({ x, y });
		p.assertValidity();
		return p;
	} catch {
		return null;
	}
}

// affine gives a point's coordinates as field elements (the hash input of
// a proof's challenge); the point at infinity is (0, 0) as in Go.
export function affine(p: GPoint): [bigint, bigint] {
	if (p.is0()) return [0n, 0n];
	const a = p.toAffine();
	return [a.x, a.y];
}

export { hex as fieldHex };
