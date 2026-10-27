import { describe, it, expect } from 'vitest';
import { newReceiver, linkURL, parseLink, seal, open } from './link';

describe('device link', () => {
	it('round-trips a sealed secret and refuses another key', () => {
		const { code, priv } = newReceiver();
		const url = linkURL('https://tartuffe.me/election/x/voter', code);
		expect(parseLink(new URL(url).hash)).toEqual(code);
		const payload = { secret: '0x' + '11'.repeat(32), position: 42, seq: 3 };
		const sealed = seal(code, payload);
		expect(open(priv, sealed)).toEqual(payload);
		const other = newReceiver();
		expect(open(other.priv, sealed)).toBeNull();
		expect(open(priv, sealed.slice(0, -4) + 'AAAA')).toBeNull();
	});
	it('rejects a malformed code', () => {
		expect(parseLink('#link=abc')).toBeNull();
		expect(parseLink('')).toBeNull();
	});
});
