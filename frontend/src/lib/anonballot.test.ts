import { describe, expect, it } from 'vitest';
import { Group } from '@semaphore-protocol/group';
import { generateProof } from '@semaphore-protocol/proof';
import { digest, message, nullifier, scopeName } from './anonballot';
import { binding, identity, recoveryWords, secretFromWords } from './voter';

const secret = '000102030405060708090a0b0c0d0e0f';

describe('anonymous ballots', () => {
	it('builds the message the server rebuilds', () => {
		const w = { series: 1, window: 9 };
		expect(message(12, w, 3, { kind: 'choices', choices: [4, 7, 9], favourite: 7 })).toBe('tartuffe-ballot/v1;poll=12;series=1;window=9;seq=3;choices=4,7,9;favourite=7');
		expect(message(12, w, 3, { kind: 'choices', choices: [4, 7, 9] })).toBe('tartuffe-ballot/v1;poll=12;series=1;window=9;seq=3;choices=4,7,9');
		expect(message(12, w, 1, { kind: 'blank', choices: [] })).toBe('tartuffe-ballot/v1;poll=12;series=1;window=9;seq=1;blank');
		expect(scopeName(12, w)).toBe('tartuffe/ballot/v1/12/1/9');
	});

	it('computes the nullifier the proof gives', async () => {
		const w = { series: 0, window: 10 };
		const me = identity(secret);
		const group = new Group([me.commitment]);
		const proof = await generateProof(me, group, digest('m'), digest(scopeName(5, w)), 20, {
			wasm: 'static/semaphore/semaphore-20.wasm',
			zkey: 'static/semaphore/semaphore-20.zkey'
		});
		expect(nullifier(secret, 5, w)).toBe(proof.nullifier);
	}, 60_000);

	it('binds the commitment as the server does', () => {
		// backend/accounts/providers.Binding("abc", "123")
		expect(binding('abc', '123')).toBe('ed584c8ea7aa5b7a8852f3f003a14ed1eb457f1a900b00d731cc54b9af16077b');
	});

	it('keeps the secret in 12 French words', () => {
		const words = recoveryWords(secret);
		expect(words).toHaveLength(12);
		expect(secretFromWords(words.join('  ').toUpperCase())).toBe(secret);
		expect(secretFromWords('pas des mots valides')).toBe('');
	});
});
