// The tally's decryption proofs, checked in the browser (SPEC §9.1, §12):
// for each component j, a Chaum-Pedersen proof that the tallier decrypted
// the sum S_j to P_j with the key behind PK_j, log_G PK_j = log_{S0}(S_j −
// P_j·G). The reference is backend/vote/ballot/proofs.go: the challenge is
// e = H(T_DLEQ, event, upto, j, G, PK_j, S0, S_j, P_j, A, B) and the checks
// z·G = A + e·PK_j, z·S0 = B + e·(S_j − P_j·G). The sums themselves are the
// auditor's check (every ballot's ciphertexts).

import { ed25519 } from '@noble/curves/ed25519.js';
import { hexToBytes } from '@noble/hashes/utils.js';
import { J, T_DLEQ, big, h } from './crypto';
import { G, ORDER, affine, decompress } from './grumpkin';
import { canonical } from './protocol';

export interface TallyBody {
	v: number;
	event: number;
	upto: number;
	c: number;
	s0: string;
	s: string[];
	p: string[];
	dleq: { a: string; b: string; z: string }[];
	// sig: the tallier's Ed25519 signature (backend/vote/sign.go).
	sig?: string;
}

// The tallier signs what it appends: "tartuffe-vote/2/tally\n" then the
// canonical body without sig, under the setup's tally_pubkey. Anyone with
// the database could append a tally otherwise.
const SIG_PREFIX = 'tartuffe-vote/2/tally\n';

// verifyTallySig returns null when the signature holds under the key,
// else what fails. A setup without a key (a trial) is "unsigned".
export function verifyTallySig(tallyPubkey: string | undefined, t: TallyBody): string | null {
	// A setup without a key (a trial log) has nothing to check: the
	// auditor warns, the page does not accuse.
	if (!tallyPubkey) return null;
	if (!/^[0-9a-f]{64}$/.test(tallyPubkey)) return 'malformed tally key';
	if (!t.sig || !/^[0-9a-f]{128}$/.test(t.sig)) return 'no tallier signature';
	const { sig, ...rest } = t;
	const msg = new TextEncoder().encode(SIG_PREFIX + canonical(rest));
	try {
		return ed25519.verify(hexToBytes(sig), msg, hexToBytes(tallyPubkey)) ? null : 'the tallier signature does not verify';
	} catch (e) {
		return `signature: ${e instanceof Error ? e.message : String(e)}`;
	}
}

// verifyTally returns null when every proof holds, else what fails (for
// the console; the page shows the results as unverified).
export function verifyTally(pk: string[], t: TallyBody): string | null {
	if (!Array.isArray(pk) || pk.length !== J) return 'the setup has no keys';
	if (!t || !Array.isArray(t.s) || !Array.isArray(t.p) || !Array.isArray(t.dleq)) return 'malformed tally';
	if (t.s.length !== J || t.p.length !== J || t.dleq.length !== J) return `expected ${J} components`;
	const s0 = decompress(t.s0);
	if (!s0) return 's0 is not a point';
	const event = BigInt(t.event);
	const upto = BigInt(t.upto);
	for (let j = 0; j < J; j++) {
		const pkj = decompress(pk[j]);
		const sj = decompress(t.s[j]);
		const a = decompress(t.dleq[j].a);
		const b = decompress(t.dleq[j].b);
		if (!pkj || !sj || !a || !b) return `component ${j}: a point is not on the curve`;
		if (!/^0x[0-9a-f]+$/i.test(t.p[j]) || !/^0x[0-9a-f]+$/i.test(t.dleq[j].z)) return `component ${j}: malformed scalar`;
		const pj = big(t.p[j]);
		const z = big(t.dleq[j].z);
		if (pj < 0n || pj >= ORDER) return `component ${j}: plaintext out of range`;
		if (z < 0n || z >= ORDER) return `component ${j}: response out of range`;
		const e = h(T_DLEQ, event, upto, BigInt(j), ...affine(G), ...affine(pkj), ...affine(s0), ...affine(sj), pj, ...affine(a), ...affine(b));
		if (!G.multiplyUnsafe(z).equals(a.add(pkj.multiplyUnsafe(e)))) return `component ${j}: the proof fails on the key`;
		const x = sj.subtract(G.multiplyUnsafe(pj));
		if (!s0.multiplyUnsafe(z).equals(b.add(x.multiplyUnsafe(e)))) return `component ${j}: the proof fails on the decryption`;
	}
	return null;
}
