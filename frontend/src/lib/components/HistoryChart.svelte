<script lang="ts">
	import type { HistoryPoint } from '$lib/types';
	import { tr, type Lang } from '$lib/i18n';

	interface Props {
		points: HistoryPoint[];
		lang: Lang;
	}

	let { points, lang }: Props = $props();

	// Geometry: one SVG, two stacked panels sharing the x scale (score on top, facts
	// per week below) and one hover crosshair across both.
	const PAD = { left: 40, right: 12, top: 8, bottom: 22 };
	const SCORE_H = 150;
	const GAP = 22;
	const FACTS_H = 70;
	const HEIGHT = PAD.top + SCORE_H + GAP + FACTS_H + PAD.bottom;

	let width = $state(0);
	let hoverIndex: number | null = $state(null);

	let plotW = $derived(Math.max(0, width - PAD.left - PAD.right));
	let n = $derived(points.length);

	// Weekly buckets are evenly spaced, so x is index-based.
	function x(i: number): number {
		return PAD.left + (n <= 1 ? plotW / 2 : (i / (n - 1)) * plotW);
	}
	function yScore(s: number): number {
		return PAD.top + (1 - s) * SCORE_H;
	}
	const factsTop = PAD.top + SCORE_H + GAP;
	let maxFacts = $derived(Math.max(1, ...points.map((p) => p.facts_added)));
	function yFacts(v: number): number {
		return factsTop + FACTS_H - (v / maxFacts) * FACTS_H;
	}

	function linePath(get: (p: HistoryPoint) => number): string {
		return points.map((p, i) => `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)},${yScore(get(p)).toFixed(1)}`).join(' ');
	}
	let cumulativePath = $derived(linePath((p) => p.score));
	let recentPath = $derived(linePath((p) => p.recent_score));

	let barW = $derived(n <= 1 ? 8 : Math.max(1, Math.min(8, (plotW / (n - 1)) * 0.7)));

	// Month ticks: first bucket of each month, thinned so labels never collide.
	let ticks = $derived.by(() => {
		const out: { i: number; label: string }[] = [];
		let lastMonth = '';
		points.forEach((p, i) => {
			const d = new Date(p.date);
			const key = `${d.getUTCFullYear()}-${d.getUTCMonth()}`;
			if (key !== lastMonth) {
				lastMonth = key;
				out.push({ i, label: monthLabel(d) });
			}
		});
		const maxLabels = Math.max(2, Math.floor(plotW / 70));
		const step = Math.ceil(out.length / maxLabels);
		return out.filter((_, k) => k % step === 0);
	});

	function locale(): string {
		return lang === 'fr' ? 'fr-FR' : 'en-GB';
	}
	function monthLabel(d: Date): string {
		return d.toLocaleDateString(locale(), { month: 'short', year: '2-digit', timeZone: 'UTC' });
	}
	function weekLabel(iso: string): string {
		return new Date(iso).toLocaleDateString(locale(), { day: 'numeric', month: 'short', year: 'numeric', timeZone: 'UTC' });
	}
	function pct(s: number): string {
		return `${(s * 100).toFixed(1)}%`;
	}

	function onMove(e: MouseEvent) {
		if (n === 0 || plotW <= 0) return;
		const rect = (e.currentTarget as SVGSVGElement).getBoundingClientRect();
		const px = e.clientX - rect.left - PAD.left;
		const i = Math.round((px / plotW) * (n - 1));
		hoverIndex = Math.min(n - 1, Math.max(0, i));
	}
	function onLeave() {
		hoverIndex = null;
	}

	let hovered = $derived(hoverIndex === null ? null : points[hoverIndex]);
	// Keep the tooltip inside the plot: flip to the left of the crosshair past the midpoint.
	let tooltipLeft = $derived(hoverIndex !== null && x(hoverIndex) > width / 2);
</script>

<div class="history-chart" bind:clientWidth={width}>
	<div class="legend">
		<span class="legend-item"><span class="swatch cumulative"></span>{tr('person.history.score', lang)}</span>
		<span class="legend-item"><span class="swatch recent"></span>{tr('person.history.recent', lang)}</span>
		<span class="legend-item"><span class="swatch facts"></span>{tr('person.history.facts', lang)}</span>
	</div>

	{#if width > 0 && n > 0}
		<!-- svelte-ignore a11y_no_static_element_interactions -->
		<svg {width} height={HEIGHT} role="img" aria-label={tr('person.history', lang)} onmousemove={onMove} onmouseleave={onLeave}>
			<!-- score panel -->
			{#each [0, 0.5, 1] as g}
				<line class="grid" x1={PAD.left} x2={PAD.left + plotW} y1={yScore(g)} y2={yScore(g)} />
				<text class="axis" x={PAD.left - 6} y={yScore(g)} text-anchor="end" dominant-baseline="middle">{Math.round(g * 100)}%</text>
			{/each}
			<path class="line recent" d={recentPath} />
			<path class="line cumulative" d={cumulativePath} />

			<!-- facts panel -->
			<line class="grid" x1={PAD.left} x2={PAD.left + plotW} y1={factsTop + FACTS_H} y2={factsTop + FACTS_H} />
			<text class="axis" x={PAD.left - 6} y={factsTop} text-anchor="end" dominant-baseline="hanging">{maxFacts}</text>
			<text class="axis" x={PAD.left - 6} y={factsTop + FACTS_H} text-anchor="end" dominant-baseline="middle">0</text>
			{#each points as p, i}
				{#if p.facts_added > 0}
					<rect
						class="bar"
						class:hover={hoverIndex === i}
						x={x(i) - barW / 2}
						y={yFacts(p.facts_added)}
						width={barW}
						height={factsTop + FACTS_H - yFacts(p.facts_added)}
						rx="1"
					/>
				{/if}
			{/each}

			<!-- x axis -->
			{#each ticks as t}
				<line class="tick" x1={x(t.i)} x2={x(t.i)} y1={factsTop + FACTS_H} y2={factsTop + FACTS_H + 4} />
				<text class="axis" x={x(t.i)} y={HEIGHT - 6} text-anchor="middle">{t.label}</text>
			{/each}

			<!-- crosshair -->
			{#if hoverIndex !== null && hovered}
				<line class="crosshair" x1={x(hoverIndex)} x2={x(hoverIndex)} y1={PAD.top} y2={factsTop + FACTS_H} />
				<circle class="marker cumulative" cx={x(hoverIndex)} cy={yScore(hovered.score)} r="4" />
				<circle class="marker recent" cx={x(hoverIndex)} cy={yScore(hovered.recent_score)} r="4" />
			{/if}
		</svg>

		{#if hoverIndex !== null && hovered}
			<div class="tooltip" style="{tooltipLeft ? 'right' : 'left'}: {tooltipLeft ? width - x(hoverIndex) + 10 : x(hoverIndex) + 10}px;">
				<div class="tooltip-date">{weekLabel(hovered.date)}</div>
				<div><span class="swatch cumulative"></span>{tr('person.history.score', lang)}: <strong>{pct(hovered.score)}</strong></div>
				<div><span class="swatch recent"></span>{tr('person.history.recent', lang)}: <strong>{pct(hovered.recent_score)}</strong></div>
				<div><span class="swatch facts"></span>{tr('person.history.facts', lang)}: <strong>{hovered.facts_added}</strong></div>
				<div class="tooltip-muted">{hovered.facts_total} {tr('person.history.facts_total', lang)} · {hovered.contradictions} {tr(hovered.contradictions === 1 ? 'count.contradiction' : 'count.contradictions', lang)}</div>
			</div>
		{/if}
	{/if}
	<p class="axis-note">{tr('person.history.axis_note', lang)}</p>
</div>

<style>
	.history-chart {
		--hist-cumulative: #b8862f;
		--hist-recent: #2f8fc4;
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

	.swatch {
		display: inline-block;
		width: 14px;
		height: 3px;
		border-radius: 2px;
		margin-right: 0.35rem;
	}
	.swatch.cumulative { background: var(--hist-cumulative); }
	.swatch.recent { background: var(--hist-recent); }
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

	.line {
		fill: none;
		stroke-width: 2;
		stroke-linejoin: round;
		stroke-linecap: round;
	}
	.line.cumulative { stroke: var(--hist-cumulative); }
	.line.recent { stroke: var(--hist-recent); stroke-dasharray: 4 3; }

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
	.marker {
		stroke: var(--bg-secondary);
		stroke-width: 2;
	}
	.marker.cumulative { fill: var(--hist-cumulative); }
	.marker.recent { fill: var(--hist-recent); }

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
		white-space: nowrap;
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
