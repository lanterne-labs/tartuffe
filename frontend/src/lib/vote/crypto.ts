// The arithmetic the browser shares with the circuit and the server
// (SPEC §3, §5, §6): Poseidon2 through bb.js, the domain tags, the leaf
// and the pseudonym, the tree hashing, the compressed point encoding, the
// values and packing of a ranking (circuits/ballot, backend/vote/ballot).

import type { BarretenbergSync } from '@aztec/bb.js';

export const N = 10;
export const BLANK = 7;
export const V = 66;
export const PER_CT = 7;
export const J = 10;
export const DEPTH = 20;
const DIGIT = 1n << 36n;
export const FR = 21888242871839275222246405745257275088548364400416034343698204186575808495617n;

export const hex = (x: bigint) => '0x' + x.toString(16).padStart(64, '0');
export const big = (h: string) => BigInt(h);
export const toBytes = (x: bigint): Uint8Array => {
	const out = new Uint8Array(32);
	for (let i = 31; i >= 0; i--) {
		out[i] = Number(x & 0xffn);
		x >>= 8n;
	}
	return out;
};
export const fromBytes = (b: Uint8Array): bigint => {
	let x = 0n;
	for (const v of b) x = (x << 8n) | BigInt(v);
	return x;
};

export function tag(name: string): bigint {
	const bytes = new TextEncoder().encode(`tartuffe-vote/2/${name}`);
	return fromBytes(bytes);
}
export const T_ID = tag('id');
export const T_NULL = tag('null');
export const T_NODE = tag('node');
export const T_REG = tag('reg');
export const T_PRF = tag('prf');
export const T_RELAY = tag('relay');
export const T_DLEQ = tag('dleq');

let api: BarretenbergSync | null = null;
let loading: Promise<BarretenbergSync> | null = null;

// bb returns the synchronous Barretenberg API (the WebAssembly module,
// loaded once).
export async function bb(): Promise<BarretenbergSync> {
	if (api) return api;
	if (!loading) {
		loading = import('@aztec/bb.js').then(async (m) => {
			api = await m.BarretenbergSync.new();
			return api;
		});
	}
	return loading;
}

// h hashes field elements with Poseidon2 (needs bb() loaded).
export function h(...xs: bigint[]): bigint {
	if (!api) throw new Error('bb not loaded');
	return fromBytes(api.poseidon2Hash({ inputs: xs.map(toBytes) }).hash);
}

export const leafOf = (event: bigint, s: bigint) => h(T_ID, event, s);
export const nullifierOf = (event: bigint, s: bigint) => h(T_NULL, event, s);
export const node = (l: bigint, r: bigint) => h(T_NODE, l, r);
// registrationBinding is the custom data the passport proof binds (SPEC
// §4): the registration key's x-only public key in two 16-byte limbs, as
// the PRF output.
export const registrationBinding = (event: bigint, key: Uint8Array) => hex(h(T_REG, event, fromBytes(key.slice(0, 16)), fromBytes(key.slice(16, 32))));

// Zero hashes per level, for empty subtrees.
let zeros: bigint[] | null = null;
export function zeroHashes(): bigint[] {
	if (!zeros) {
		zeros = [0n];
		for (let i = 1; i <= DEPTH; i++) zeros[i] = node(zeros[i - 1], zeros[i - 1]);
	}
	return zeros;
}

// rootFromPath recomputes a root as the circuit does.
export function rootFromPath(leaf: bigint, path: bigint[], index: number): bigint {
	let cur = leaf;
	for (let i = 0; i < DEPTH; i++) cur = (index >> i) & 1 ? node(path[i], cur) : node(cur, path[i]);
	return cur;
}

// compress encodes a point as x with the parity of y in the top bit.
export function compress(x: bigint, y: bigint): string {
	if (x >> 255n) throw new Error('x has the top bit set');
	return hex(y & 1n ? x | (1n << 255n) : x);
}

// Ranking is what the voter expressed: len options in order.
export interface Ranking {
	len: number;
	slots: [number, number, number];
}

export function values(r: Ranking): number[] {
	const pos = Array(N).fill(3);
	for (let k = 0; k < r.len; k++) pos[r.slots[k]] = k;
	const v = Array(V).fill(0);
	if (r.len >= 1) v[0] = 1;
	for (let c = 0; c < N; c++) {
		if (r.len >= 1 && r.slots[0] === c) v[1 + c] = 1;
		if (pos[c] < 3) v[1 + N + c] = 1;
	}
	let idx = 1 + 2 * N;
	for (let i = 0; i < N; i++) for (let j = i + 1; j < N; j++) v[idx++] = pos[i] < pos[j] ? 1 : pos[j] < pos[i] ? -1 : 0;
	return v;
}

export function pack(v: number[]): bigint[] {
	const p: bigint[] = [];
	for (let j = 0; j < J; j++) {
		let acc = 0n,
			w = 1n;
		for (let k = 0; k < PER_CT; k++) {
			const idx = PER_CT * j + k;
			acc += (idx < V ? BigInt(v[idx] + 1) : 1n) * w;
			w *= DIGIT;
		}
		p.push(acc);
	}
	return p;
}

// unpack decodes a sum over count ballots.
export function unpack(p: bigint[], count: bigint): number[] {
	const out: number[] = [];
	for (let j = 0; j < J; j++) {
		let x = p[j];
		for (let k = 0; k < PER_CT; k++) {
			const d = x & (DIGIT - 1n);
			x >>= 36n;
			const idx = PER_CT * j + k;
			if (d < 0n || d > 2n * count) throw new Error(`digit ${k} of ${j} out of range`);
			if (idx < V) out[idx] = Number(d - count);
		}
	}
	return out;
}

// validRanking applies the rules under the setup's mask.
export function validRanking(r: Ranking, mask: number): string | null {
	if (r.len < 0 || r.len > 3) return 'at most 3 options';
	for (let k = 0; k < 3; k++) {
		const o = r.slots[k];
		if (k >= r.len) {
			if (o !== 0) return 'unused slot';
			continue;
		}
		if (o < 0 || o >= N || !((mask >> o) & 1)) return 'option not allowed';
		for (let j = 0; j < k; j++) if (r.slots[j] === o) return 'option twice';
		if (o === BLANK && k !== r.len - 1) return 'blank must be last';
	}
	return null;
}

// randomField draws a nonzero field element.
export function randomField(): bigint {
	const b = new Uint8Array(32);
	crypto.getRandomValues(b);
	const x = fromBytes(b) % FR;
	return x === 0n ? 1n : x;
}
