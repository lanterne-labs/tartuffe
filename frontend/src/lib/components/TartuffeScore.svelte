<script lang="ts">
	// The Tartuffe-Score as a barometer: a continuous track from coherent
	// (green) to tartuffe (red), a marker where the person sits, and under it
	// "Tartuffe-Score: X/100" with a small "?" saying what a higher score
	// means. The evidence (contradictions over facts) says how solid it is.
	// Too few facts is an empty dashed track, no marker and "not enough facts".
	import { tr, type Lang } from '$lib/i18n';

	interface Props {
		/** The honesty score as the API gives it, 0 to 1 (1 is coherent); null or undefined when unknown. */
		honesty: number | null | undefined;
		lang: Lang;
		/** sm: a list row or a card; md: a block; lg: a page header. */
		size?: 'sm' | 'md' | 'lg';
		/** The score out of 100 and its "?" under the track. */
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
	// The marker keeps off the very ends so that 0 and 100 still show it whole.
	let at = $derived(tartuffe === null ? 0 : 3 + (tartuffe / 100) * 94);
	let value = $derived(
		unrated ? (facts !== undefined ? tr('score.unrated.n', lang, { f: facts }) : tr('score.unrated', lang)) : tr('score.short', lang, { n: tartuffe ?? 0 })
	);
	let evidence = $derived.by(() => {
		if (unrated || facts === undefined || contradictions === undefined) return '';
		return tr(contradictions === 1 ? 'score.evidence.one' : 'score.evidence', lang, { c: contradictions, f: facts });
	});

	// The small "?": one sentence on what the number means. It can sit inside
	// a card that is a link, so its clicks stay here.
	let help = $state(false);
	let root: HTMLElement | undefined = $state();
	function toggle(e: MouseEvent) {
		e.preventDefault();
		e.stopPropagation();
		help = !help;
	}
	// A click in the bubble stays in it, except on its own link.
	function keep(e: MouseEvent) {
		e.stopPropagation();
		if (!(e.target as Element | null)?.closest('a')) e.preventDefault();
	}
	function outside(e: MouseEvent) {
		if (help && root && !root.contains(e.target as Node)) help = false;
	}
</script>

<script lang="ts" module>
	/** The Tartuffe value, 0 (coherent) to 100 (tartuffe), of an honesty score 0 to 1. */
	export function tartuffeOf(honesty: number | null | undefined): number | null {
		return typeof honesty === 'number' && Number.isFinite(honesty) ? Math.round((1 - honesty) * 100) : null;
	}
</script>

<svelte:window onclick={outside} onkeydown={(e) => e.key === 'Escape' && (help = false)} />

<span class="ts ts--{size}" class:ts--unrated={unrated}>
	{#if caption}<span class="caption">{caption}</span>{/if}
	<span class="track" role="img" aria-label={evidence ? `${value}. ${evidence}` : value}>
		{#if !unrated}
			<span class="marker" class:marker--in={animate} style="left: {at}%"></span>
		{/if}
	</span>
	{#if named}
		<span class="value" bind:this={root}>
			<span class:value--none={unrated}>{value}</span>
			{#if !unrated}
				<button class="q" onclick={toggle} aria-expanded={help} aria-label={tr('score.help', lang)}>?</button>
				{#if help}
					<!-- svelte-ignore a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
					<span class="tip" role="note" onclick={keep}>
						{tr('score.tip', lang)}
						<a href="/about#score">{tr('score.tip.more', lang)}</a>
					</span>
				{/if}
			{/if}
		</span>
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
		border-radius: 999px;
		background: linear-gradient(90deg, var(--green), var(--amber) 50%, var(--red));
	}
	.ts--unrated .track {
		background: transparent;
		border: 1px dashed var(--glass-border);
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
	.value {
		position: relative;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: center;
		gap: 0.3rem;
		margin-top: 0.2rem;
		font-size: 0.74rem;
		font-weight: 700;
		line-height: 1.2;
		color: var(--text-secondary);
		font-variant-numeric: tabular-nums;
	}
	.ts--sm .value {
		justify-content: flex-start;
	}
	.ts--md .value {
		font-size: 0.85rem;
	}
	.ts--lg .value {
		font-size: 1.05rem;
		color: var(--text-primary);
	}
	.value--none {
		color: var(--text-muted);
		font-weight: 600;
		font-style: italic;
	}
	.q {
		width: 1.05rem;
		height: 1.05rem;
		padding: 0;
		border-radius: 50%;
		border: 1px solid var(--glass-border);
		background: transparent;
		color: var(--text-muted);
		font-size: 0.65rem;
		font-weight: 700;
		line-height: 1;
		cursor: pointer;
	}
	.q:hover,
	.q[aria-expanded='true'] {
		color: var(--text-primary);
		border-color: var(--text-muted);
	}
	.tip {
		position: absolute;
		top: calc(100% + 0.4rem);
		left: 50%;
		transform: translateX(-50%);
		z-index: 20;
		width: min(16rem, 80vw);
		padding: 0.6rem 0.75rem;
		border-radius: var(--radius-md, 10px);
		background: var(--bg-elevated, #1c1a22);
		border: 1px solid var(--glass-border);
		box-shadow: 0 6px 24px rgba(0, 0, 0, 0.5);
		font-size: 0.8rem;
		font-weight: 500;
		font-style: normal;
		line-height: 1.4;
		color: var(--text-primary);
		text-align: center;
	}
	.ts--sm .tip {
		left: 0;
		transform: none;
		text-align: left;
	}
	/* In a page block the bubble opens in the flow: a card that clips its
	   overflow would cut it. */
	.ts--md .tip,
	.ts--lg .tip {
		position: static;
		transform: none;
		flex-basis: 100%;
		margin: 0.4rem auto 0;
	}
	.tip a {
		display: block;
		margin-top: 0.35rem;
		font-weight: 600;
		color: var(--accent-hover, var(--accent));
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
