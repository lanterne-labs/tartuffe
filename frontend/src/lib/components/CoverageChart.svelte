<script lang="ts">
	import type { Coverage } from '$lib/types';
	import { tr, type Lang } from '$lib/i18n';

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
	// The history chart beside this one has a range row and a two-row legend
	// above its panels: the taller panel here brings the two axis notes level.
	const BARS_H = 312;
	const HEIGHT = PAD.top + BARS_H + PAD.bottom;

	let width = $state(0);
	let hoverIndex: number | null = $state(null);

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

	function onMove(e: MouseEvent) {
		if (n === 0 || slotW <= 0) return;
		const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
		const px = e.clientX - rect.left - PAD.left;
		hoverIndex = Math.min(n - 1, Math.max(0, Math.floor(px / slotW)));
	}
	function onLeave() {
		hoverIndex = null;
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

	{#if width > 0 && n > 0}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<svg {width} height={HEIGHT} role="img" aria-label={tr('person.coverage', lang)} onmousemove={onMove} onmouseleave={onLeave}>
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

		{#if hoverIndex !== null && hovered}
			<div class="tooltip" style="left: {tooltipX}px; max-width: {TOOLTIP_W}px;">
				<div class="tooltip-date">{hovered.year}</div>
				<div><span class="swatch facts"></span>{tr('person.coverage.facts', lang)}: <strong>{hovered.facts}</strong></div>
				<div class="tooltip-muted">{share(hovered.facts)} {tr('person.coverage.share', lang)} · {coverage.total} {tr('person.history.facts_total', lang)}</div>
			</div>
		{/if}
	{/if}
	<p class="axis-note">{tr('person.coverage.axis_note', lang)}</p>
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
		font-size: 10px;
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

	.axis-note {
		margin: 0.5rem 0 0;
		font-size: 0.7rem;
		color: var(--text-muted);
	}
</style>
