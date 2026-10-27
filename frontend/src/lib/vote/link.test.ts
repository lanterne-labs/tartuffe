import { describe, it, expect } from 'vitest';
import { newReceiver, linkURL, parseLink, seal, open, confirmCode } from './link';

describe('device link', () => {
	it('round-trips a sealed secret, with the same code on both sides, and refuses another key', () => {
		const { code, priv } = newReceiver();
		const url = linkURL('https://tartuffe.me/election/x/voter', code);
		expect(parseLink(new URL(url).hash)).toEqual(code);
		const payload = { secret: '0x' + '11'.repeat(32), position: 42, seq: 3, slots: [1, 7], words: 'abc def' };
		const sent = seal(code, payload);
		expect(sent.code).toMatch(/^\d{6}$/);
		const got = open(priv, sent.sealed);
		expect(got?.payload).toEqual(payload);
		expect(got?.code).toBe(sent.code);
		// Another sending to the same receiver gives another code: the code
		// names the pair of keys, not the receiver alone.
		expect(seal(code, payload).code).not.toBe(sent.code);
		const other = newReceiver();
		expect(open(other.priv, sent.sealed)).toBeNull();
		expect(open(priv, sent.sealed.slice(0, -4) + 'AAAA')).toBeNull();
	});
	it('derives six digits from a shared key', () => {
		const k = new Uint8Array(32).fill(7);
		expect(confirmCode(k)).toMatch(/^\d{6}$/);
		expect(confirmCode(k)).toBe(confirmCode(new Uint8Array(32).fill(7)));
		expect(confirmCode(k)).not.toBe(confirmCode(new Uint8Array(32).fill(8)));
	});
	it('rejects a malformed code', () => {
		expect(parseLink('#link=abc')).toBeNull();
		expect(parseLink('')).toBeNull();
	});
});
