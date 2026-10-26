#!/usr/bin/env node
// Proves one ballot end to end and checks the circuit against an
// independent JavaScript computation of the encryption, then writes the
// test vectors of SPEC §14 (vectors/dev.json, committed), the witness for
// `bb` (out/witness.gz) and the inputs the browser benchmark loads
// (out/inputs.json).
//
// Run from frontend/ (for its node_modules):
//   node ../circuits/ballot/scripts/vectors.mjs [circuit dir]
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { randomBytes } from 'node:crypto';
import { resolve } from 'node:path';
import { Noir } from '@noir-lang/noir_js';
import { Barretenberg, BarretenbergSync, UltraHonkBackend, GRUMPKIN_G1_GENERATOR } from '@aztec/bb.js';

const dir = resolve(process.argv[2] ?? '../circuits/ballot');
const circuit = JSON.parse(readFileSync(`${dir}/target/ballot.json`, 'utf8'));

// The circuit's constants (SPEC §6, §13).
const N = 10, BLANK = 7, V = 66, PER_CT = 7, J = 10, D = 20;
const DIGIT = 1n << 36n;
const R = 21888242871839275222246405745257275088548364400416034343698204186575808495617n; // BN254 Fr
const tag = (name) => BigInt('0x' + Buffer.from(`tartuffe-vote/2/${name}`, 'ascii').toString('hex'));
const T_ID = tag('id'), T_NULL = tag('null'), T_NODE = tag('node');

const hex = (x) => '0x' + x.toString(16).padStart(64, '0');
const toBuf = (x) => Uint8Array.from(Buffer.from(x.toString(16).padStart(64, '0'), 'hex'));
const fromBuf = (b) => BigInt('0x' + Buffer.from(b).toString('hex'));
const rand = () => fromBuf(randomBytes(32)) % R;

const api = await BarretenbergSync.new();
const h = (...xs) => fromBuf(api.poseidon2Hash({ inputs: xs.map(toBuf) }).hash);
const vector = h(1n, 2n, 3n);
if (hex(vector) !== '0x23864adb160dddf590f1d3303683ebcb914f828e2635f6e85a32f0a1aecd3dd8') {
	throw new Error(`poseidon2 mismatch with the circuit: ${hex(vector)}`);
}

// Grumpkin arithmetic, for the independent check of the ciphertexts.
const pt = (x, y) => ({ x: toBuf(x), y: toBuf(y) });
const G = { x: GRUMPKIN_G1_GENERATOR.x, y: GRUMPKIN_G1_GENERATOR.y };
const mul = (p, k) => api.grumpkinMul({ point: p, scalar: toBuf(k) }).point;
const add = (a, b) => api.grumpkinAdd({ pointA: a, pointB: b }).point;
const keys = readFileSync(`${dir}/dev-keys.txt`, 'utf8').trim().split('\n').map((l) => BigInt(l.trim()));
const PK = Array.from({ length: J }, (_, j) => pt(keys[2 * j], keys[2 * j + 1]));

// The tree: zero hashes, a few voters, ours at `index`.
const zero = [0n];
for (let i = 1; i <= D; i++) zero[i] = h(T_NODE, zero[i - 1], zero[i - 1]);
function root_and_path(leaves, index) {
	let level = leaves.slice();
	const path = [];
	let idx = index;
	for (let i = 0; i < D; i++) {
		const sib = idx ^ 1;
		path.push(sib < level.length ? level[sib] : zero[i]);
		const next = [];
		for (let k = 0; k < level.length; k += 2) {
			const l = level[k], r = k + 1 < level.length ? level[k + 1] : zero[i];
			next.push(h(T_NODE, l, r));
		}
		level = next;
		idx >>= 1;
	}
	return { root: level.length ? level[0] : zero[D], path };
}

// The values of SPEC §6.1, as digits v + 1.
function digits(len, slots) {
	const pos = Array(N).fill(3);
	for (let k = 0; k < len; k++) pos[slots[k]] = k;
	const d = Array(V).fill(1n);
	d[0] = len >= 1 ? 2n : 1n;
	for (let c = 0; c < N; c++) {
		d[1 + c] = len >= 1 && slots[0] === c ? 2n : 1n;
		d[1 + N + c] = pos[c] < 3 ? 2n : 1n;
	}
	let v = 1 + 2 * N;
	for (let i = 0; i < N; i++)
		for (let j = i + 1; j < N; j++) d[v++] = pos[i] < pos[j] ? 2n : pos[j] < pos[i] ? 0n : 1n;
	return d;
}
function pack(d) {
	const p = [];
	for (let j = 0; j < J; j++) {
		let acc = 0n, w = 1n;
		for (let k = 0; k < PER_CT; k++) {
			const idx = PER_CT * j + k;
			acc += (idx < V ? d[idx] : 1n) * w;
			w *= DIGIT;
		}
		p.push(acc);
	}
	return p;
}

// One ballot: 4 > 2 > blank, voter at index 3 of 5, seq 1.
const event = 1n, setup = 5n, mask = 255, seq = 1;
const s = rand(), r = rand() || 1n;
const id = h(T_ID, event, s);
const others = Array.from({ length: 4 }, rand).map((x) => h(T_ID, event, x));
const leaves = [others[0], others[1], others[2], id, others[3]];
const index = 3;
const { root, path } = root_and_path(leaves, index);
const nullifier = h(T_NULL, event, s);
const slots = [4, 2, BLANK], len = 3;

const inputs = {
	event: hex(event), setup: hex(setup), mask, root: hex(root), nullifier: hex(nullifier), seq,
	s: hex(s), path: path.map(hex), index, slots, len, r: hex(r),
};
const noir = new Noir(circuit);
let t = performance.now();
const { witness, returnValue } = await noir.execute(inputs);
console.log(`witness ${(performance.now() - t).toFixed(0)} ms`);

// The same ciphertexts, computed outside the circuit.
const p = pack(digits(len, slots));
const expect = [mul(G, r)];
for (let j = 0; j < J; j++) expect.push(add(mul(G, p[j]), mul(PK[j], r)));
const got = returnValue.map((x) => BigInt(x));
for (let i = 0; i <= J; i++) {
	if (got[2 * i] !== fromBuf(expect[i].x) || got[2 * i + 1] !== fromBuf(expect[i].y)) {
		throw new Error(`ciphertext ${i} differs from the JavaScript computation`);
	}
}
console.log('ciphertexts match the independent computation');

const threads = Number(process.env.THREADS ?? 8);
const bb = await Barretenberg.new({ threads });
const backend = new UltraHonkBackend(circuit.bytecode, bb);
t = performance.now();
const proof = await backend.generateProof(witness, { verifierTarget: 'noir-recursive' });
console.log(`prove ${(performance.now() - t).toFixed(0)} ms (${threads} threads), proof ${proof.proof.length} B, ${proof.publicInputs.length} public inputs`);
t = performance.now();
const ok = await backend.verifyProof(proof, { verifierTarget: 'noir-recursive' });
console.log(`verify ${(performance.now() - t).toFixed(0)} ms ok=${ok}`);
if (!ok) throw new Error('proof does not verify');
const vk = await backend.getVerificationKey({ verifierTarget: 'noir-recursive' });
await bb.destroy();

mkdirSync(`${dir}/out`, { recursive: true });
mkdirSync(`${dir}/vectors`, { recursive: true });
writeFileSync(`${dir}/out/witness.gz`, witness);
writeFileSync(`${dir}/out/inputs.json`, JSON.stringify(inputs));
writeFileSync(
	`${dir}/vectors/dev.json`,
	JSON.stringify(
		{
			poseidon2: { inputs: ['0x1', '0x2', '0x3'], hash: hex(vector) },
			tags: { T_ID: hex(T_ID), T_NULL: hex(T_NULL), T_NODE: hex(T_NODE) },
			tree: { depth: D, leaves: leaves.map(hex), index, root: hex(root), path: path.map(hex) },
			ballot: { inputs, digits: digits(len, slots).map(String), packed: p.map(hex), public_inputs: proof.publicInputs, ciphertexts: returnValue },
			proof: Buffer.from(proof.proof).toString('base64'),
			vk_sha256: (await import('node:crypto')).createHash('sha256').update(vk).digest('hex'),
		},
		null,
		1
	)
);
console.log(`wrote ${dir}/vectors/dev.json and ${dir}/out/{witness.gz,inputs.json}`);
