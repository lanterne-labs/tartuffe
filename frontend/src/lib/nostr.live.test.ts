import { describe, expect, it } from 'vitest';
import { schnorr } from '@noble/curves/secp256k1.js';
import { hexToBytes } from '@noble/hashes/utils.js';
import { ballotEvent, publish } from './nostr';

const relays = ['wss://nos.lol', 'wss://relay.primal.net', 'wss://offchain.pub', 'wss://nostr.mom', 'wss://relay.snort.social'];

describe.skipIf(!process.env.NOSTR_LIVE)('relays', () => {
	it('take a ballot event', async () => {
		const ev = ballotEvent(JSON.stringify({ test: 'tartuffe relay check, ignore', at: Date.now() }));
		expect(schnorr.verify(hexToBytes(ev.sig), hexToBytes(ev.id), hexToBytes(ev.pubkey))).toBe(true);
		const ok = await publish(relays, ev);
		console.log('took it:', ok);
		// read back
		for (const r of ok) {
			const got = await new Promise<number>((res) => {
				const ws = new WebSocket(r);
				let n = 0;
				const t = setTimeout(() => res(-1), 8000);
				ws.onopen = () => ws.send(JSON.stringify(['REQ', 's', { kinds: [7464], '#t': ['tartuffe-ballot-v1'], ids: [ev.id] }]));
				ws.onmessage = (m) => { const msg = JSON.parse(String(m.data)); if (msg[0] === 'EVENT') n++; if (msg[0] === 'EOSE') { clearTimeout(t); ws.close(); res(n); } };
			});
			console.log(r, 'reads it back:', got);
		}
	}, 30000);
});
