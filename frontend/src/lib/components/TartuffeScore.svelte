<script lang="ts">
	// The Tartuffe-Score: five bands, A to E, green to red, like the
	// nutrition letters everyone reads at a glance. A is coherent, E is
	// tartuffe. No number as the headline: the band carries the verdict
	// and the direction; the evidence (contradictions over facts) says how
	// solid it is. Without enough facts there is no band, only "unrated".
	import { tr, type Lang } from '$lib/i18n';

	interface Props {
		/** The honesty score as the API gives it, 0 to 1; null or undefined when unknown. */
		honesty: number | null | undefined;
		lang: Lang;
		/** sm: the letters alone (a card); md: with the end labels. */
		size?: 'sm' | 'md';
		/** Below this many facts the person is unrated; undefined counts as enough (the API does not say). */
		facts?: number;
	}
	let { honesty, lang, size = 'sm', facts }: Props = $props();

	const LETTERS = ['A', 'B', 'C', 'D', 'E'] as const;
	const COLORS = ['var(--green)', 'color-mix(in oklch, var(--amber) 50%, var(--green))', 'var(--amber)', 'color-mix(in oklch, var(--red) 50%, var(--amber))', 'var(--red)'];
	// The cut points, on the Tartuffe side (100 − honesty): fixed, so a
	// person's letter moves only when their own record does.
	const CUTS = [10, 25, 50, 75];
	const MIN_FACTS = 10;

	let tartuffe = $derived(typeof honesty === 'number' && Number.isFinite(honesty) ? Math.round((1 - honesty) * 100) : null);
	let unrated = $derived(tartuffe === null || (facts !== undefined && facts < MIN_FACTS));
	let band = $derived(tartuffe === null ? -1 : CUTS.findIndex((c) => tartuffe! < c) === -1 ? 4 : CUTS.findIndex((c) => tartuffe! < c));
	let label = $derived(unrated ? tr('score.unrated', lang) : tr('score.band', lang, { letter: LETTERS[band], n: tartuffe ?? 0 }));
</script>

<span class="ts ts--{size}" class:ts--unrated={unrated} role="img" aria-label={label} title={label}>
	{#if size === 'md'}<span class="end">{tr('score.end.coherent', lang)}</span>{/if}
	<span class="bands">
		{#each LETTERS as letter, i (letter)}
			<span class="band" class:band--on={!unrated && i === band} style="--c: {COLORS[i]}">{letter}</span>
		{/each}
	</span>
	{#if size === 'md'}<span class="end">{tr('score.end.tartuffe', lang)}</span>{/if}
</span>

<style>
	.ts {
		display: inline-flex;
		align-items: center;
		gap: 0.45rem;
		font-weight: 700;
		line-height: 1;
	}
	.bands {
		display: inline-flex;
		align-items: center;
		gap: 2px;
	}
	.band {
		display: grid;
		place-items: center;
		width: 1.15em;
		height: 1.15em;
		border-radius: 0.25em;
		background: var(--c);
		color: #0a0a0f;
		font-size: 0.8rem;
		opacity: 0.28;
		transform: scale(0.82);
		transition: opacity 0.2s, transform 0.2s;
	}
	.band--on {
		opacity: 1;
		transform: scale(1.18);
		box-shadow: 0 0 0 2px var(--bg-card), 0 0 0 3.5px var(--c);
	}
	.ts--md .band {
		font-size: 1rem;
	}
	.ts--unrated .band {
		background: transparent;
		color: var(--text-muted);
		border: 1px dashed var(--glass-border);
		opacity: 0.6;
		transform: none;
	}
	.end {
		font-size: 0.68rem;
		font-weight: 600;
		letter-spacing: 0.06em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
</style>
