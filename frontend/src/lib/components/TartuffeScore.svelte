<script lang="ts">
	// The Tartuffe-Score as a dial ("la règle"): a band over the top half of a
	// circle fills from green to red up to the person's place, graduations cut
	// into it, a white needle on the place. The portrait sits in the circle
	// (or, without a person, the number). Under it, the score out of 100 in
	// the colour of the place, with a small "?" saying what a higher score
	// means. Too few facts: a grey band, no needle, "not enough facts".
	//
	// The dial is logarithmic in contradictions (dialOf): the first
	// contradictions move the needle a lot, each later one a little less, so
	// the graduations (one every few contradictions) crowd towards the red.
	import { tr, type Lang } from '$lib/i18n';

	interface Props {
		/** The honesty score as the API gives it, 0 to 1 (1 is coherent); null or undefined when unknown. */
		honesty: number | null | undefined;
		lang: Lang;
		/** xs: a chip or a feed line; sm: a list row or a card; md: a block; lg: a page header. */
		size?: 'xs' | 'sm' | 'md' | 'lg';
		/** The portrait in the circle; without it, the initial, else the number. */
		image?: string | null;
		initial?: string;
		/** The score out of 100 and its "?" under the dial. */
		named?: boolean;
		/** Below MIN_FACTS facts the dial is unrated. */
		facts?: number;
		/** A caption over the dial (an own score, a members' score). */
		caption?: string;
		/** The needle sweeps in from the coherent end (an example being told). */
		animate?: boolean;
	}
	let { honesty, lang, size = 'sm', image = null, initial = '', named = true, facts, caption = '', animate = false }: Props = $props();

	const uid = $props.id();
	// Below this many facts the score says too little to be shown.
	const MIN_FACTS = 10;
	let tartuffe = $derived(tartuffeOf(honesty));
	let unrated = $derived(tartuffe === null || (facts !== undefined && facts < MIN_FACTS));
	let at = $derived(unrated || tartuffe === null ? 0 : dialOf(tartuffe));
	let colour = $derived(unrated ? 'var(--text-muted)' : tartuffeColor(at));
	// The graduations: one every 3 contradictions, every 6 on a small dial.
	let marks = $derived(MARKS.filter((_, i) => size === 'lg' || size === 'md' || i % 2 === 0));
	let centreNumber = $derived(!image && !initial);
	let value = $derived(
		unrated
			? facts !== undefined
				? tr('score.unrated.n', lang, { f: facts })
				: tr('score.unrated', lang)
			: size === 'lg' || size === 'md'
				? tr('score.short', lang, { n: tartuffe ?? 0 })
				: `${tartuffe}/100`
	);

	// Polar point on the dial: r from the centre (60, 60), at a place 0 to 100.
	function pt(r: number, place: number): [number, number] {
		const a = Math.PI * (1 - place / 100);
		return [60 + r * Math.cos(a), 60 - r * Math.sin(a)];
	}
	const BAND = 'M11 60 A49 49 0 0 1 109 60';

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

	// The score of c distinct contradictions is about 100 × (1 − exp(−K·c))
	// for a person with 2000 facts (k = 4, j = 0.02 in the formula).
	const K = 4 / 2000 + 0.02;
	// The dial ends at 150 contradictions; BEND softens the logarithm so the
	// first graduations are not too far apart.
	const CMAX = 150;
	const BEND = 3;
	function placeOfContradictions(c: number): number {
		return (100 * Math.log1p(c / BEND)) / Math.log1p(CMAX / BEND);
	}
	/** Where a Tartuffe value sits on the dial, 0 to 100. */
	export function dialOf(t: number): number {
		const c = -Math.log(1 - Math.min(t, 99.9) / 100) / K;
		return Math.min(100, placeOfContradictions(c));
	}
	/** The places of the graduations: one every 3 contradictions. */
	export const MARKS = Array.from({ length: CMAX / 3 + 1 }, (_, i) => placeOfContradictions(i * 3));
	/** The colour of a place on the dial (or a bar): green, amber, red. */
	export function tartuffeColor(place: number): string {
		return place < 50
			? `color-mix(in oklch, var(--green) ${Math.round(100 - place * 2)}%, var(--amber))`
			: `color-mix(in oklch, var(--amber) ${Math.round(100 - (place - 50) * 2)}%, var(--red))`;
	}
</script>

<svelte:window onclick={outside} onkeydown={(e) => e.key === 'Escape' && (help = false)} />

<span class="ts ts--{size}" class:ts--unrated={unrated} style="--c: {colour}">
	{#if caption}<span class="caption">{caption}</span>{/if}
	<svg viewBox="0 0 120 102" class="dial" role="img" aria-label={value}>
		<defs>
			<linearGradient id="{uid}-g" x1="11" x2="109" gradientUnits="userSpaceOnUse">
				<stop offset="0" style="stop-color: var(--green)" />
				<stop offset=".5" style="stop-color: var(--amber)" />
				<stop offset="1" style="stop-color: var(--red)" />
			</linearGradient>
			<clipPath id="{uid}-c"><circle cx="60" cy="60" r="40" /></clipPath>
		</defs>
		<path d={BAND} class="track" />
		{#if !unrated}
			<path d={BAND} class="fill" class:fill--in={animate} stroke="url(#{uid}-g)" pathLength="100" style="--at: {at}" />
		{/if}
		{#each marks as m, i (i)}
			{@const [x1, y1] = pt(44.5, m)}
			{@const [x2, y2] = pt(53.5, m)}
			<line {x1} {y1} {x2} {y2} class="notch" class:notch--long={i % 10 === 0} />
		{/each}
		{#if image}
			<image href={image} x="20" y="20" width="80" height="80" preserveAspectRatio="xMidYMid slice" clip-path="url(#{uid}-c)" />
		{:else}
			<circle cx="60" cy="60" r="40" class="face" />
			{#if initial}
				<text x="60" y="73" text-anchor="middle" class="initial">{initial}</text>
			{:else if !unrated}
				<text x="60" y="72" text-anchor="middle" class="centre">{tartuffe}</text>
			{/if}
		{/if}
		{#if !unrated}
			<!-- The needle is drawn at 0 and turned to its place. -->
			<g class="needle" class:needle--in={animate} style="--turn: {at * 1.8}deg">
				<line x1="3" y1="60" x2="17.5" y2="60" />
			</g>
		{/if}
	</svg>
	{#if named && !(centreNumber && !unrated && (size === 'xs' || size === 'sm'))}
		<span class="value" bind:this={root}>
			<span class:value--none={unrated}>{value}</span>
			{#if !unrated && size !== 'xs'}
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
</span>

<style>
	.ts {
		display: inline-flex;
		flex-direction: column;
		align-items: center;
		gap: 0.2rem;
		width: var(--w);
		max-width: 100%;
		--w: 5.75rem;
	}
	.ts--xs {
		--w: 3.6rem;
	}
	.ts--md {
		--w: 9rem;
	}
	.ts--lg {
		--w: 13rem;
	}
	.caption {
		font-size: 0.7rem;
		font-weight: 600;
		color: var(--text-secondary);
		text-align: center;
	}
	.dial {
		display: block;
		width: 100%;
		height: auto;
		overflow: visible;
	}
	.track,
	.fill {
		fill: none;
		stroke-width: 8;
	}
	.track {
		stroke: rgba(255, 255, 255, 0.1);
	}
	.ts--unrated .track {
		stroke-dasharray: 3 3;
		stroke: rgba(255, 255, 255, 0.14);
	}
	.fill {
		stroke-dasharray: var(--at) 100;
	}
	.notch {
		stroke: var(--bg-card, #141419);
		stroke-width: 0.9;
	}
	.notch--long {
		stroke-width: 1.6;
	}
	.ts--unrated .notch {
		display: none;
	}
	.face {
		fill: var(--bg-elevated, #1c1a22);
	}
	.initial {
		font-size: 34px;
		font-weight: 700;
		fill: var(--text-muted);
		font-family: var(--font-family);
	}
	.centre {
		font-size: 36px;
		font-weight: 800;
		fill: var(--c);
		font-family: var(--font-family);
		font-variant-numeric: tabular-nums;
	}
	.needle {
		transform-box: view-box;
		transform-origin: 60px 60px;
		transform: rotate(var(--turn));
	}
	.needle line {
		stroke: #fff;
		stroke-width: 2.6;
		stroke-linecap: round;
	}
	.needle--in {
		animation: sweep 1.4s cubic-bezier(0.22, 1, 0.36, 1) 0.3s both;
	}
	.fill--in {
		animation: fill 1.4s cubic-bezier(0.22, 1, 0.36, 1) 0.3s both;
	}
	@keyframes sweep {
		from {
			transform: rotate(0deg);
		}
	}
	@keyframes fill {
		from {
			stroke-dasharray: 0 100;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.needle--in,
		.fill--in {
			animation: none;
		}
	}
	.value {
		position: relative;
		width: max-content;
		max-width: 90vw;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: center;
		gap: 0.3rem;
		font-size: 0.78rem;
		font-weight: 800;
		line-height: 1.2;
		color: var(--c);
		font-variant-numeric: tabular-nums;
		white-space: nowrap;
	}
	.ts--xs .value {
		font-size: 0.68rem;
	}
	.ts--md .value {
		font-size: 0.92rem;
	}
	.ts--lg .value {
		font-size: 1.1rem;
	}
	.value--none {
		color: var(--text-muted);
		font-weight: 600;
		font-style: italic;
		white-space: normal;
		text-align: center;
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
		line-height: 1.4;
		color: var(--text-primary);
		text-align: center;
		white-space: normal;
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
</style>
