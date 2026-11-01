<script lang="ts">
	// The Tartuffe-Score as a barometer: a track of five levels from coherent
	// (green) to completely tartuffe (red), a marker where the person sits,
	// and the name of the level under it. No number as the headline: the
	// level says it, and the evidence under it (contradictions over facts)
	// says how solid it is. Too few facts is an empty dashed track, no marker
	// and "not enough facts". The number out of 100 is in the tooltip and the
	// methodology, never the headline.
	import { tr, type Lang } from '$lib/i18n';

	interface Props {
		/** The honesty score as the API gives it, 0 to 1 (1 is coherent); null or undefined when unknown. */
		honesty: number | null | undefined;
		lang: Lang;
		/** sm: a list row or a card; md: a block; lg: a page header. */
		size?: 'sm' | 'md' | 'lg';
		/** The name of the level under the track. */
		named?: boolean;
		/** The evidence line, shown when both counts are given. */
		facts?: number;
		contradictions?: number;
		/** A caption over the track (an own score, a members' score). */
		caption?: string;
		/** The evidence line under the track (off where the page says the counts already). */
		showEvidence?: boolean;
		/** The marker slides in from the coherent end (an example being told). */
		animate?: boolean;
	}
	let { honesty, lang, size = 'sm', named = true, facts, contradictions, caption = '', showEvidence = true, animate = false }: Props = $props();

	// Below this many facts the score says too little to be shown.
	const MIN_FACTS = 10;
	let tartuffe = $derived(tartuffeOf(honesty));
	let unrated = $derived(tartuffe === null || (facts !== undefined && facts < MIN_FACTS));
	let level = $derived(tartuffe === null ? 0 : levelOf(tartuffe));
	// The marker keeps off the very ends so that 0 and 100 still show it whole.
	let at = $derived(tartuffe === null ? 0 : 3 + positionOf(tartuffe) * 94);
	let name = $derived(
		unrated ? (facts !== undefined ? tr('score.unrated.n', lang, { f: facts }) : tr('score.unrated', lang)) : tr(`score.level.${level}`, lang)
	);
	let evidence = $derived.by(() => {
		if (unrated || facts === undefined || contradictions === undefined) return '';
		return tr(contradictions === 1 ? 'score.evidence.one' : 'score.evidence', lang, { c: contradictions, f: facts });
	});
	let label = $derived(unrated ? name : `${name}. ${tr('score.value', lang, { n: tartuffe ?? 0 })}`);
</script>

<script lang="ts" module>
	/** The Tartuffe value, 0 (coherent) to 100 (tartuffe), of an honesty score 0 to 1. */
	export function tartuffeOf(honesty: number | null | undefined): number | null {
		return typeof honesty === 'number' && Number.isFinite(honesty) ? Math.round((1 - honesty) * 100) : null;
	}
	/** The lower bounds of the five levels, on the Tartuffe value 0 to 100. */
	export const LEVELS = [0, 5, 15, 30, 50];
	/** The level of a Tartuffe value: 0 coherent to 4 completely tartuffe. */
	export function levelOf(t: number): number {
		let i = 0;
		while (i + 1 < LEVELS.length && t >= LEVELS[i + 1]) i++;
		return i;
	}
	/** Where a Tartuffe value sits on the track, 0 to 1: each level takes a fifth. */
	function positionOf(t: number): number {
		const i = levelOf(t);
		const lo = LEVELS[i];
		const hi = LEVELS[i + 1] ?? 100;
		return (i + Math.min(1, (t - lo) / (hi - lo))) / LEVELS.length;
	}
</script>

<span class="ts ts--{size}" class:ts--unrated={unrated} role="img" aria-label={evidence ? `${label}. ${evidence}` : label} title={evidence ? `${label}. ${evidence}` : label}>
	{#if caption}<span class="caption">{caption}</span>{/if}
	<span class="track">
		{#each LEVELS as _, i}<span class="seg seg--{i}" class:seg--on={!unrated && i === level}></span>{/each}
		{#if !unrated}
			<span class="marker" class:marker--in={animate} style="left: {at}%"></span>
		{/if}
	</span>
	{#if named}
		<span class="name name--{unrated ? 'none' : level}">{name}</span>
	{/if}
	{#if evidence && showEvidence}
		<span class="evidence">{evidence}</span>
	{/if}
</span>

<style>
	.ts {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		width: 100%;
		min-width: 5.5rem;
	}
	.ts--md {
		max-width: 18rem;
	}
	.ts--lg {
		max-width: 22rem;
		gap: 0.35rem;
	}
	.caption {
		font-size: 0.7rem;
		font-weight: 600;
		color: var(--text-secondary);
		text-align: center;
	}
	.track {
		position: relative;
		height: 0.42rem;
		margin: 0.3rem 0 0;
		display: flex;
		gap: 2px;
	}
	/* Five levels, green to red; the level the person is in is lit. */
	.seg {
		flex: 1;
		opacity: 0.55;
	}
	.seg:first-child {
		border-radius: 999px 0 0 999px;
	}
	.seg:last-child {
		border-radius: 0 999px 999px 0;
	}
	.seg--on {
		opacity: 1;
	}
	.seg--0, .name--0 { --c: var(--green); }
	.seg--1, .name--1 { --c: color-mix(in oklch, var(--green) 55%, var(--amber)); }
	.seg--2, .name--2 { --c: var(--amber); }
	.seg--3, .name--3 { --c: color-mix(in oklch, var(--amber) 45%, var(--red)); }
	.seg--4, .name--4 { --c: var(--red); }
	.seg {
		background: var(--c);
	}
	.ts--md .track {
		height: 0.5rem;
	}
	.ts--lg .track {
		height: 0.6rem;
	}
	.ts--unrated .track {
		border: 1px dashed var(--glass-border);
		border-radius: 999px;
	}
	.ts--unrated .seg {
		display: none;
	}
	.marker {
		position: absolute;
		top: 50%;
		width: 0.95rem;
		height: 0.95rem;
		border-radius: 50%;
		background: #fff;
		border: 2.5px solid var(--bg-primary, #0a0a0f);
		box-shadow:
			0 0 0 1.5px #fff,
			0 1px 4px rgba(0, 0, 0, 0.6);
		transform: translate(-50%, -50%);
	}
	.marker--in {
		animation: slide-in 1.4s cubic-bezier(0.22, 1, 0.36, 1) 0.3s both;
	}
	@keyframes slide-in {
		from {
			left: 3%;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.marker--in {
			animation: none;
		}
	}
	.ts--lg .marker {
		width: 1.25rem;
		height: 1.25rem;
	}
	.name {
		margin-top: 0.2rem;
		font-size: 0.74rem;
		font-weight: 700;
		line-height: 1.2;
		color: var(--c);
		text-align: center;
	}
	.ts--sm .name {
		text-align: left;
	}
	.ts--md .name {
		font-size: 0.85rem;
	}
	.ts--lg .name {
		font-size: 1.05rem;
	}
	.name--none {
		color: var(--text-muted);
		font-weight: 600;
		font-style: italic;
	}
	.evidence {
		font-size: 0.74rem;
		line-height: 1.3;
		color: var(--text-secondary);
		text-align: center;
	}
	.ts--sm .evidence {
		text-align: left;
	}
</style>
