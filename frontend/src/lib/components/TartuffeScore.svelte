<script lang="ts">
	// The Tartuffe-Score as a barometer: a track from coherent (green) to
	// tartuffe (red), the two words at its ends, and a marker where the
	// person sits. No number as the headline: the position says it, and
	// the evidence under it (contradictions over facts) says how solid it
	// is. Zero contradictions is the marker at the far left. Too few facts
	// is an empty dashed track and no marker. The number out of 100 is in
	// the tooltip and the methodology, never the headline.
	import { tr, type Lang } from '$lib/i18n';

	interface Props {
		/** The honesty score as the API gives it, 0 to 1 (1 is coherent); null or undefined when unknown. */
		honesty: number | null | undefined;
		lang: Lang;
		/** sm: a list row or a card; md: a block; lg: a page header. */
		size?: 'sm' | 'md' | 'lg';
		/** The two words at the ends of the track. */
		ends?: boolean;
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
	let { honesty, lang, size = 'sm', ends = true, facts, contradictions, caption = '', showEvidence = true, animate = false }: Props = $props();

	// Below this many facts the score says too little to be shown.
	const MIN_FACTS = 10;
	let tartuffe = $derived(tartuffeOf(honesty));
	let unrated = $derived(tartuffe === null || (facts !== undefined && facts < MIN_FACTS));
	// The marker keeps off the very ends so that 0 and 100 still show it whole.
	let at = $derived(tartuffe === null ? 0 : 3 + (tartuffe / 100) * 94);
	let evidence = $derived.by(() => {
		if (facts === undefined) return '';
		if (unrated) return tr('score.unrated.n', lang, { f: facts });
		if (contradictions === undefined) return '';
		return tr(contradictions === 1 ? 'score.evidence.one' : 'score.evidence', lang, { c: contradictions, f: facts });
	});
	let label = $derived(unrated ? tr('score.unrated', lang) : tr('score.value', lang, { n: tartuffe ?? 0 }));
</script>

<script lang="ts" module>
	/** The Tartuffe value, 0 (coherent) to 100 (tartuffe), of an honesty score 0 to 1. */
	export function tartuffeOf(honesty: number | null | undefined): number | null {
		return typeof honesty === 'number' && Number.isFinite(honesty) ? Math.round((1 - honesty) * 100) : null;
	}
	/** The colour of a Tartuffe value on the track: green, amber, red. */
	export function tartuffeColor(t: number): string {
		return t < 50
			? `color-mix(in oklch, var(--green) ${Math.round(100 - t * 2)}%, var(--amber))`
			: `color-mix(in oklch, var(--amber) ${Math.round(100 - (t - 50) * 2)}%, var(--red))`;
	}
</script>

<span class="ts ts--{size}" class:ts--unrated={unrated} role="img" aria-label={evidence ? `${label}. ${evidence}` : label} title={evidence ? `${label}. ${evidence}` : label}>
	{#if caption}<span class="caption">{caption}</span>{/if}
	<span class="track">
		{#if !unrated}
			<span class="marker" class:marker--in={animate} style="left: {at}%"></span>
		{/if}
	</span>
	{#if ends}
		<span class="ends"><span>{tr('score.end.coherent', lang)}</span><span>{tr('score.end.tartuffe', lang)}</span></span>
	{/if}
	{#if evidence && (showEvidence || unrated)}
		<span class="evidence" class:evidence--unrated={unrated}>{evidence}</span>
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
	.ts--md .track {
		height: 0.5rem;
	}
	.ts--lg .track {
		height: 0.6rem;
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
	.ends {
		display: flex;
		justify-content: space-between;
		font-size: 0.58rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
	.ts--lg .ends {
		font-size: 0.66rem;
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
	.evidence--unrated {
		color: var(--text-muted);
		font-style: italic;
	}
</style>
