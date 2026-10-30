// The registration key (SPEC §4): the binding the passport proof carries
// and the signature over the body, against the Go vector
// (backend/vote/regsign_test.go: secret 0x11…11, event 7).
import { describe, it, expect, beforeAll } from 'vitest';
import { bytesToHex } from '@noble/hashes/utils.js';
import { bb, registrationBinding } from './crypto';
import { registerBody, registrationRequest, regPubkey, verifyRegisterSig, canonical, type RegisterBody } from './protocol';
import type { VoteKey } from './key';

const SECRET = '11'.repeat(32);

describe('registration key', () => {
	beforeAll(async () => {
		await bb();
	});

	it('binds the key as the server does', () => {
		expect(bytesToHex(regPubkey(SECRET))).toBe('4f355bdcb7cc0af728ef3cceb9615d90684bb5b2ca5f859ab0f0b704075871aa');
		expect(registrationBinding(7n, regPubkey(SECRET))).toBe('0x1337ef0b840d3a978286919c650ce217a3f1db50235b8ef8ac909a4a8b44f4f6');
		expect(registrationRequest(SECRET, 7, 'test').customData).toBe('0x1337ef0b840d3a978286919c650ce217a3f1db50235b8ef8ac909a4a8b44f4f6');
	});

	it('signs the body under the key, and a changed leaf no longer verifies', () => {
		const key = { eventId: 7, leaf: '0x' + '00'.repeat(31) + '2a', regSecret: SECRET } as VoteKey;
		const body = JSON.parse(registerBody(key, 'uid-vector', { proofs: [], result: { a: 1 } }, new Date('2026-10-30T12:00:00Z'))) as RegisterBody;
		expect(body.key).toBe('4f355bdcb7cc0af728ef3cceb9615d90684bb5b2ca5f859ab0f0b704075871aa');
		expect(verifyRegisterSig(body)).toBe(true);
		expect(verifyRegisterSig({ ...body, id: '0x' + '00'.repeat(31) + '2b' })).toBe(false);
		expect(verifyRegisterSig({ ...body, sig: undefined })).toBe(false);
	});

	it('writes canonical JSON as Go does', () => {
		expect(canonical({ b: 'a<b>&c', a: [1, { z: 1, y: 2 }] })).toBe('{"a":[1,{"y":2,"z":1}],"b":"a\\u003cb\\u003e\\u0026c"}');
	});
});
