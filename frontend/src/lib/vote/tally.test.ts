// The tally's decryption proofs, checked as the page does (tally.ts),
// against a tally the ingest's tallier published on a local mirror
// (testdata/tally-vectors.json: the setup's keys and the tally body).
import { describe, it, expect, beforeAll } from 'vitest';
import vectors from './testdata/tally-vectors.json';
import { bb } from './crypto';
import { verifyTally, verifyTallySig, type TallyBody } from './tally';
import { G, decompress, affine } from './grumpkin';

const tally = vectors.tally as TallyBody;

describe('the tally check', () => {
	beforeAll(async () => {
		await bb();
	});

	it('decodes points and the generator', () => {
		expect(affine(G)).toEqual([1n, 17631683881184975370165255887551781615748388533673675138860n]);
		expect(decompress(tally.s0)).not.toBeNull();
		expect(decompress('0x' + 'ff'.repeat(32))).toBeNull();
		expect(decompress('0x' + '00'.repeat(32))).toBeNull();
	});

	it('accepts the tallier’s proofs', () => {
		expect(verifyTally(vectors.pk, tally)).toBeNull();
	});

	it('refuses a changed plaintext, response or key', () => {
		const p = structuredClone(tally);
		p.p[0] = '0x' + (BigInt(p.p[0]) + 1n).toString(16);
		expect(verifyTally(vectors.pk, p)).toMatch(/component 0/);
		const z = structuredClone(tally);
		z.dleq[3].z = '0x' + (BigInt(z.dleq[3].z) ^ 1n).toString(16);
		expect(verifyTally(vectors.pk, z)).toMatch(/component 3/);
		const pk = [...vectors.pk];
		[pk[0], pk[1]] = [pk[1], pk[0]];
		expect(verifyTally(pk, tally)).toMatch(/component 0/);
		const upto = { ...tally, upto: tally.upto + 1 };
		expect(verifyTally(vectors.pk, upto)).toMatch(/component 0/);
	});
});

describe('the tallier signature', () => {
	// The vector of backend/vote/sign_test.go: seed 0x11…11.
	const pub = 'd04ab232742bb4ab3a1368bd4615e4e6d0224ab71a016baf8520a332c9778737';
	const body = { c: 3, dleq: [{ a: '0x04', b: '0x05', z: '0x06' }], event: 1, p: ['0x03'], s: ['0x02'], s0: '0x01', sig: '2dbc7386457bf22eac1b84921b4b7932b26f25966013437235c171f674a20bc811085ede9a12405cee08e0f8e70da194ef1af7b3848dfb43ad964d426f8ef306', upto: 5, v: 2 };
	it('verifies the Go vector and refuses changes', () => {
		expect(verifyTallySig(pub, body)).toBeNull();
		expect(verifyTallySig(pub, { ...body, upto: 6 })).not.toBeNull();
		expect(verifyTallySig(pub, { ...body, sig: '00' + body.sig.slice(2) })).not.toBeNull();
		expect(verifyTallySig(undefined, body)).toMatch(/unsigned/);
		expect(verifyTallySig(pub, { ...body, sig: undefined })).toMatch(/no tallier/);
	});
});
