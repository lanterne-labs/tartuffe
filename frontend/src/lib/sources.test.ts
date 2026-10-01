import { describe, expect, it } from 'vitest';
import { outletSummary, sourceDomain } from './sources';

describe('sourceDomain', () => {
	it('shows the outlet domain without www', () => {
		expect(sourceDomain('https://www.lefigaro.fr/politique/article-123')).toBe('lefigaro.fr');
		expect(sourceDomain('https://actu.orange.fr/politique/x')).toBe('actu.orange.fr');
	});
	it('shows the account of an X post', () => {
		expect(sourceDomain('https://x.com/jlmelenchon/status/1')).toBe('x.com/@jlmelenchon');
		expect(sourceDomain('https://twitter.com/mlp_officiel')).toBe('x.com/@mlp_officiel');
	});
	it('keeps an unparsable source as is', () => {
		expect(sourceDomain('meta://group/42')).toBe('meta://group/42');
		expect(sourceDomain('not a url')).toBe('not a url');
	});
});

describe('outletSummary', () => {
	it('lists distinct outlets, the first ones plus a count', () => {
		const urls = [
			'https://www.lemonde.fr/a',
			'https://www.lemonde.fr/b',
			'https://www.liberation.fr/c',
			'https://www.tf1info.fr/d',
			'https://x.com/mlp_officiel/status/1',
			''
		];
		expect(outletSummary(urls)).toEqual({ shown: ['lemonde.fr', 'liberation.fr', 'tf1info.fr'], more: 1 });
		expect(outletSummary(urls.slice(0, 2))).toEqual({ shown: ['lemonde.fr'], more: 0 });
	});
});
