import { describe, expect, it } from 'vitest';
import { byName, nameKey } from './candidates';

describe('nameKey', () => {
	it('ignores case, spaces, Unicode form and accents', () => {
		expect(nameKey('Jean-Luc  Mélenchon ')).toBe('jean-luc melenchon');
		expect(nameKey('jean-luc melenchon')).toBe('jean-luc melenchon');
		expect(nameKey('Édouard Philippe')).toBe(nameKey('Édouard Philippe'));
		expect(nameKey('Raphaël Glucksmann')).toBe('raphael glucksmann');
		expect(nameKey('François Ruffin')).toBe('francois ruffin');
	});

	it('finds a card whose name gained its accents after the poll was logged', () => {
		const cards = byName([{ name: 'Jean-Luc Mélenchon' } as never]);
		expect(cards.get(nameKey('jean-luc melenchon'))).toBeDefined();
	});
});
