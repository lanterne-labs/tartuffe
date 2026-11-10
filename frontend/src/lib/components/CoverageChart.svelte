<script lang="ts">
	import type { Coverage } from '$lib/types';
	import { tr, type Lang } from '$lib/i18n';
	import HintSheet from './HintSheet.svelte';

	interface Props {
		coverage: Coverage;
		lang: Lang;
	}

	let { coverage, lang }: Props = $props();
	let years = $derived(coverage.years);
	let n = $derived(years.length);

	// Geometry: the same frame as the history chart beside it (its padding, its
	// axis), one panel of bars, one per year.
	const PAD = { left: 40, right: 12, top: 8, bottom: 22 };

	let width = $state(0);
	let hoverIndex: number | null = $state(null);

	// The history chart beside this one has a range row and a two-row legend
	// above its panels: the taller panel here brings the two axis notes level.
	// On a phone the charts are stacked, not side by side: a panel that tall
	// is a screen of bars, so it is shorter there.
	let BARS_H = $derived(width > 0 && width < 480 ? 200 : 312);
	let HEIGHT = $derived(PAD.top + BARS_H + PAD.bottom);

	let plotW = $derived(Math.max(0, width - PAD.left - PAD.right));
	// Years are evenly spaced, so x is index-based: the centre of the year's slot.
	let slotW = $derived(n === 0 ? 0 : plotW / n);
	function x(i: number): number {
		return PAD.left + (i + 0.5) * slotW;
	}
	let barW = $derived(Math.max(2, slotW * 0.7));
	let maxFacts = $derived(Math.max(1, ...years.map((y) => y.facts)));
	function yFacts(v: number): number {
		return PAD.top + BARS_H - (v / maxFacts) * BARS_H;
	}
	let gridValues = $derived([maxFacts, Math.round(maxFacts / 2), 0]);

	// Year ticks, thinned so labels never collide.
	let ticks = $derived.by(() => {
		const maxLabels = Math.max(2, Math.floor(plotW / 44));
		const step = Math.ceil(n / maxLabels);
		return years.map((y, i) => ({ i, label: String(y.year) })).filter((_, k) => k % step === 0);
	});

	function locale(): string {
		return lang === 'fr' ? 'fr-FR' : 'en-GB';
	}
	function share(v: number): string {
		return coverage.total === 0 ? '0%' : `${((v / coverage.total) * 100).toLocaleString(locale(), { maximumFractionDigits: 1 })}%`;
	}

	function indexAt(e: MouseEvent): number | null {
		if (n === 0 || slotW <= 0) return null;
		const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
		const px = e.clientX - rect.left - PAD.left;
		return Math.min(n - 1, Math.max(0, Math.floor(px / slotW)));
	}
	function onMove(e: MouseEvent) {
		hoverIndex = indexAt(e) ?? hoverIndex;
	}
	function onLeave() {
		hoverIndex = null;
	}

	// A touch screen has no hover: a tap picks the nearest year, marks it with
	// the crosshair and opens its details in a sheet. Read after mount: the
	// page is server-rendered.
	let touch = $state(false);
	$effect(() => {
		touch = window.matchMedia('(hover: none)').matches;
	});
	let sheet = $state(false);
	function onTap(e: MouseEvent) {
		if (!touch) return;
		const i = indexAt(e);
		if (i === null) return;
		hoverIndex = i;
		sheet = true;
	}

	let hovered = $derived(hoverIndex === null ? null : years[hoverIndex]);
	const TOOLTIP_W = 230;
	let tooltipX = $derived.by(() => {
		if (hoverIndex === null) return 0;
		const cx = x(hoverIndex);
		if (cx + 10 + TOOLTIP_W <= width) return cx + 10;
		return Math.max(0, cx - 10 - TOOLTIP_W);
	});
</script>

<div class="coverage-chart" bind:clientWidth={width}>
	<div class="legend">
		<span class="legend-item"><span class="swatch facts"></span>{tr('person.coverage.facts', lang)}</span>
		{#if coverage.undated > 0}
			<span class="legend-item legend-muted">{coverage.undated} {tr('person.coverage.undated', lang)}</span>
		{/if}
	</div>

	{#snippet details(y: Coverage['years'][number])}
		<div class="tooltip-date">{y.year}</div>
		<div><span class="swatch facts"></span>{tr('person.coverage.facts', lang)}: <strong>{y.facts}</strong></div>
		<div class="tooltip-muted">{share(y.facts)} {tr('person.coverage.share', lang)} · {coverage.total} {tr('person.history.facts_total', lang)}</div>
	{/snippet}

	{#if width > 0 && n > 0}
		<!-- svelte-ignore a11y_no_static_element_interactions, a11y_click_events_have_key_events, a11y_no_noninteractive_element_interactions -->
		<svg {width} height={HEIGHT} role="img" aria-label={tr('person.coverage', lang)} onmousemove={onMove} onmouseleave={onLeave} onclick={onTap}>
			{#each gridValues as g, k}
				<line class="grid" x1={PAD.left} x2={PAD.left + plotW} y1={yFacts(g)} y2={yFacts(g)} />
				<text class="axis" x={PAD.left - 6} y={yFacts(g)} text-anchor="end" dominant-baseline={k === 0 ? 'hanging' : 'middle'}>{g}</text>
			{/each}
			{#each years as y, i}
				{#if y.facts > 0}
					<rect
						class="bar"
						class:hover={hoverIndex === i}
						x={x(i) - barW / 2}
						y={yFacts(y.facts)}
						width={barW}
						height={PAD.top + BARS_H - yFacts(y.facts)}
						rx="1"
					/>
				{/if}
			{/each}

			<!-- x axis -->
			{#each ticks as t}
				<line class="tick" x1={x(t.i)} x2={x(t.i)} y1={PAD.top + BARS_H} y2={PAD.top + BARS_H + 4} />
				<text class="axis" x={x(t.i)} y={HEIGHT - 6} text-anchor="middle">{t.label}</text>
			{/each}

			{#if hoverIndex !== null}
				<line class="crosshair" x1={x(hoverIndex)} x2={x(hoverIndex)} y1={PAD.top} y2={PAD.top + BARS_H} />
			{/if}
		</svg>

		<!-- The hover tooltip is the mouse's; a tap gets the sheet instead. -->
		{#if !touch && hoverIndex !== null && hovered}
			<div class="tooltip" style="left: {tooltipX}px; max-width: {TOOLTIP_W}px;">
				{@render details(hovered)}
			</div>
		{/if}
		{#if touch && hovered}
			<HintSheet open={sheet} onclose={() => (sheet = false)}>
				<div class="sheet-details">{@render details(hovered)}</div>
			</HintSheet>
		{/if}
	{/if}
	<p class="axis-note">{tr('person.coverage.axis_note', lang)}</p>
	<p class="ongoing">{tr('person.coverage.ongoing', lang)}</p>
</div>

<style>
	/* The same tokens as HistoryChart: the two charts sit side by side. */
	.coverage-chart {
		--hist-facts: #7d7168;
		position: relative;
		width: 100%;
	}

	.legend {
		display: flex;
		flex-wrap: wrap;
		gap: 1rem;
		font-size: 0.75rem;
		color: var(--text-secondary);
		margin-bottom: 0.5rem;
	}

	.legend-item {
		display: inline-flex;
		align-items: center;
		gap: 0.4rem;
	}
	.legend-muted { color: var(--text-muted); }

	.swatch {
		display: inline-block;
		border-radius: 2px;
		margin-right: 0.35rem;
	}
	.swatch.facts { background: var(--hist-facts); height: 8px; width: 8px; }

	svg {
		display: block;
		overflow: visible;
	}

	.grid {
		stroke: var(--border-card-hover);
		stroke-width: 1;
	}
	.tick {
		stroke: var(--text-muted);
		stroke-width: 1;
	}
	.axis {
		fill: var(--text-muted);
		font-size: 11px;
	}

	.bar {
		fill: var(--hist-facts);
		opacity: 0.85;
	}
	.bar.hover { opacity: 1; fill: var(--text-secondary); }

	.crosshair {
		stroke: var(--text-muted);
		stroke-width: 1;
		stroke-dasharray: 2 2;
	}

	.tooltip {
		position: absolute;
		top: 2rem;
		pointer-events: none;
		background: var(--bg-secondary);
		border: 1px solid var(--border-card-hover);
		border-radius: 6px;
		padding: 0.5rem 0.65rem;
		font-size: 0.75rem;
		color: var(--text-secondary);
		line-height: 1.5;
		z-index: 2;
	}
	.tooltip strong { color: var(--text-primary); }
	.tooltip-date { color: var(--text-primary); font-weight: 600; margin-bottom: 0.15rem; }
	.tooltip-muted { color: var(--text-muted); margin-top: 0.15rem; }
	/* The same lines in the sheet of a touch screen, read as a list. */
	.sheet-details {
		text-align: left;
		font-size: 0.9rem;
		color: var(--text-secondary);
	}
	.sheet-details strong { color: var(--text-primary); }

	.axis-note {
		margin: 0.5rem 0 0;
		font-size: 0.75rem;
		color: var(--text-muted);
	}
	.ongoing {
		margin: 0.6rem 0 0;
		padding-top: 0.6rem;
		border-top: 1px solid var(--border-card);
		font-size: 0.8rem;
		line-height: 1.5;
		color: var(--text-secondary);
	}
</style>
