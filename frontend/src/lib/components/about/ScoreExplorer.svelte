<script lang="ts">
	// An interactive chart of the Tartuffe-Score: the reader picks a number
	// of distinct contradictions (B), of facts (F) and of extra pairs inside
	// the bundles (E), and sees the score, its barometer and its curve over
	// B change live. The formula and its constants are those of $lib/score
	// (backend/core/honesty.go): Tartuffe-Score = 100 · (1 − honesty). The
	// candidates' average rate is today's, read from the API.
	import type { Lang } from '$lib/i18n';
	import TartuffeScore from '$lib/components/TartuffeScore.svelte';
	import { onMount } from 'svelte';
	import { API_BASE } from '$lib/config';
	import { DECAY, DEFAULT_RATE, EXTRA_WEIGHT, SMOOTHING, formatConstant, rateFromCovered, scoreTerms } from '$lib/score';

	let { lang }: { lang: Lang } = $props();

	const B_MAX = 40;
	const FEW = 500;
	const factOptions = [FEW, 5000, 20000];

	// The candidates' contradictions per fact: every person carries the facts
	// from which one is covered (SMOOTHING contradictions expected).
	let rate = $state(DEFAULT_RATE);
	onMount(async () => {
		try {
			const res = await fetch(`${API_BASE}/api/persons?candidate=true&limit=1`);
			if (!res.ok) return;
			const persons: { covered_facts?: number }[] = await res.json();
			rate = rateFromCovered(persons[0]?.covered_facts);
		} catch (err) {
			console.warn('score explorer: the candidates\' rate was not read, the default stays', err);
		}
	});

	function score(b: number, f: number, e: number, r: number): number {
		if (f <= 0) return 0;
		return 100 * (1 - scoreTerms({ facts: f, bundles: b, pairs: b + e }, r).score);
	}

	let b = $state(1);
	let f = $state(5000);
	let e = $state(0);

	let value = $derived(score(b, f, e, rate));
	let expected = $derived(rate * f);
	let shown = $derived(Math.round(value));

	// Chart geometry (viewBox units).
	const W = 360;
	const H = 210;
	const L = 34;
	const R = 14;
	const T = 14;
	const Bm = 30;
	const x = (v: number) => L + (v / B_MAX) * (W - L - R);
	const y = (v: number) => T + (1 - v / 100) * (H - T - Bm);

	function curve(fv: number, ev: number, r: number): string {
		let d = '';
		for (let i = 0; i <= B_MAX; i += 0.5) {
			d += `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)},${y(score(i, fv, ev, r)).toFixed(1)}`;
		}
		return d;
	}

	let mainPath = $derived(curve(f, e, rate));
	let areaPath = $derived(`${mainPath}L${x(B_MAX)},${y(0)}L${x(0)},${y(0)}Z`);
	let ghosts = $derived(factOptions.filter((o) => o !== f).map((o) => ({ f: o, d: curve(o, e, rate) })));

	let tone = $derived(value < 20 ? 'var(--green)' : value < 50 ? 'var(--amber)' : 'var(--red)');

	const text = {
		fr: {
			contradictions: 'Contradictions distinctes (B)',
			facts: 'Faits connus (F)',
			extra: 'Paires en plus dans les mêmes lots (E)',
			score: 'Tartuffe-Score',
			axisX: 'contradictions distinctes',
			few: 'Avec peu de faits, on n’attend presque aucune contradiction : c’est leur nombre qui compte, et un score bas veut surtout dire que l’on sait encore peu de choses sur cette personne.',
			expected: 'contradictions attendues pour ces faits, au taux moyen des candidats',
			many: 'Qui parle beaucoup finit par se contredire un peu : avec beaucoup de faits, c’est le taux de contradictions comparé à la moyenne des candidats qui compte.',
			alt: 'Courbe du Tartuffe-Score en fonction du nombre de contradictions'
		},
		en: {
			contradictions: 'Distinct contradictions (B)',
			facts: 'Known facts (F)',
			extra: 'Extra pairs inside the same bundles (E)',
			score: 'Tartuffe-Score',
			axisX: 'distinct contradictions',
			few: 'With few facts, almost no contradiction is expected: their count decides, and a low score mostly means we still know little about this person.',
			expected: 'contradictions expected of these facts, at the candidates’ average rate',
			many: 'Whoever speaks a lot ends up contradicting themselves a little: with many facts, what counts is the rate of contradictions against the candidates’ average.',
			alt: 'Curve of the Tartuffe-Score as a function of the number of contradictions'
		}
	};
	let c = $derived(text[lang]);
	let k = $derived([DECAY, EXTRA_WEIGHT].map((n) => formatConstant(n, lang)));
	let expectedText = $derived(expected.toLocaleString(lang, { maximumFractionDigits: 1 }));
</script>

<div class="explorer">
	<div class="controls">
		<!-- A − and a + beside each slider: a finger cannot set it to the unit. -->
		<div class="ctl">
			<span class="ctl-head"><span id="b-label">{c.contradictions}</span><output>{b}</output></span>
			<div class="slider">
				<button type="button" class="step" aria-label="−1" onclick={() => (b = Math.max(0, b - 1))}>−</button>
				<input type="range" min="0" max={B_MAX} step="1" bind:value={b} aria-labelledby="b-label" />
				<button type="button" class="step" aria-label="+1" onclick={() => (b = Math.min(B_MAX, b + 1))}>+</button>
			</div>
		</div>

		<div class="ctl">
			<span class="ctl-head"><span id="facts-label">{c.facts}</span></span>
			<div class="toggles" role="radiogroup" aria-labelledby="facts-label">
				{#each factOptions as o}
					<button
						type="button"
						role="radio"
						aria-checked={f === o}
						class:on={f === o}
						onclick={() => (f = o)}>{o.toLocaleString(lang)}</button
					>
				{/each}
			</div>
		</div>

		<div class="ctl">
			<span class="ctl-head"><span id="e-label">{c.extra}</span><output>{e}</output></span>
			<div class="slider">
				<button type="button" class="step" aria-label="−1" onclick={() => (e = Math.max(0, e - 1))}>−</button>
				<input type="range" min="0" max="30" step="1" bind:value={e} aria-labelledby="e-label" />
				<button type="button" class="step" aria-label="+1" onclick={() => (e = Math.min(30, e + 1))}>+</button>
			</div>
		</div>

		<div class="readout" style:--tone={tone}>
			<span class="big">{shown}</span><span class="out">/100</span>
		</div>
		<TartuffeScore honesty={1 - value / 100} {lang} size="md" />
		<p class="formula" aria-hidden="true">
			100 × (1 − exp(−{k[0]} × ({b} + {k[1]}×ln(1+{e})) / ({expectedText} + {SMOOTHING})))
		</p>
		<p class="hint"><b>{expectedText}</b> {c.expected}. {f <= FEW ? c.few : c.many}</p>
	</div>

	<figure class="chart">
		<svg viewBox="0 0 {W} {H}" role="img" aria-label={c.alt}>
			<defs>
				<linearGradient id="score-area" x1="0" x2="0" y1="0" y2="1">
					<stop offset="0%" stop-color="var(--accent)" stop-opacity="0.35" />
					<stop offset="100%" stop-color="var(--accent)" stop-opacity="0" />
				</linearGradient>
			</defs>
			{#each [0, 25, 50, 75, 100] as t}
				<line class="grid" x1={L} x2={W - R} y1={y(t)} y2={y(t)} />
				<text class="tick" x={L - 6} y={y(t) + 3} text-anchor="end">{t}</text>
			{/each}
			{#each [0, 10, 20, 30, 40] as t}
				<text class="tick" x={x(t)} y={H - Bm + 14} text-anchor="middle">{t}</text>
			{/each}
			<text class="axis" x={(L + W - R) / 2} y={H - 3} text-anchor="middle">{c.axisX}</text>

			<!-- The curves meet at the right end: each label sits on its curve
			     at its own B, so they never cover each other. -->
			{#each ghosts as g, i}
				{@const gb = B_MAX - 10 * i}
				<path class="ghost" d={g.d} />
				<text class="ghost-label" x={x(gb) - 2} y={y(score(gb, g.f, e, rate)) - 4} text-anchor="end"
					>{`F = ${g.f.toLocaleString(lang)}`}</text
				>
			{/each}
			<path class="area" d={areaPath} fill="url(#score-area)" />
			<path class="main" d={mainPath} />

			<line class="drop" x1={x(b)} x2={x(b)} y1={y(value)} y2={y(0)} />
			<circle class="halo" cx={x(b)} cy={y(value)} r="9" style:--tone={tone} />
			<circle class="dot" cx={x(b)} cy={y(value)} r="4.5" style:--tone={tone} />
		</svg>
	</figure>
</div>

<style>
	.explorer {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1.25fr);
		gap: 1.5rem;
		align-items: center;
		padding: 1.25rem;
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		border: 1px solid var(--border-card);
	}
	.controls {
		display: flex;
		flex-direction: column;
		gap: 0.9rem;
		min-width: 0;
	}
	.ctl {
		display: flex;
		flex-direction: column;
		gap: 0.35rem;
	}
	.ctl-head {
		display: flex;
		justify-content: space-between;
		gap: 0.5rem;
		font-size: 0.8rem;
		color: var(--text-secondary);
	}
	output {
		font-variant-numeric: tabular-nums;
		color: var(--text-primary);
		font-weight: 600;
	}
	.slider {
		display: flex;
		align-items: center;
		gap: 0.25rem;
	}
	input[type='range'] {
		flex: 1;
		min-width: 0;
		width: 100%;
		accent-color: var(--accent);
	}
	.step {
		flex-shrink: 0;
		display: grid;
		place-items: center;
		width: 2.5rem;
		height: 2.5rem;
		padding: 0;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
		background: transparent;
		color: var(--text-secondary);
		font: inherit;
		font-size: 1.1rem;
		line-height: 1;
		cursor: pointer;
		transition: all var(--transition-fast);
	}
	.step:hover {
		border-color: var(--border-card-hover);
		color: var(--text-primary);
	}
	.toggles {
		display: flex;
		gap: 0.35rem;
	}
	.toggles button {
		flex: 1;
		padding: 0.35rem 0.5rem;
		border-radius: var(--radius-full);
		border: 1px solid var(--glass-border);
		background: transparent;
		color: var(--text-secondary);
		font-size: 0.85rem;
		font-variant-numeric: tabular-nums;
		cursor: pointer;
		transition: all var(--transition-fast);
	}
	.toggles button:hover {
		border-color: var(--border-card-hover);
		color: var(--text-primary);
	}
	.toggles button.on {
		background: var(--accent);
		border-color: var(--accent);
		color: white;
	}
	@media (pointer: coarse) {
		.toggles button {
			min-height: 2.5rem;
		}
	}
	.readout {
		display: flex;
		align-items: baseline;
		justify-content: center;
		gap: 0.2rem;
		margin-top: 0.25rem;
	}
	/* The dial's label is wider than the dial and centred on it: at the left
	   edge of the column it would stick out of the card. */
	.controls :global(.ts) {
		align-self: center;
	}
	.big {
		font-family: var(--font-serif);
		font-variant-numeric: lining-nums tabular-nums;
		font-size: 3.5rem;
		font-weight: 600;
		line-height: 1;
		color: var(--tone);
		transition: color var(--transition-normal);
	}
	.out {
		color: var(--text-muted);
		font-size: 1rem;
	}
	.formula {
		font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
		font-size: 0.78rem;
		color: var(--text-muted);
		overflow-wrap: anywhere;
		margin: 0;
	}
	.hint {
		font-size: 0.85rem;
		color: var(--text-secondary);
		line-height: 1.5;
		margin: 0;
		min-height: 3.8em;
	}
	.chart {
		margin: 0;
		min-width: 0;
	}
	svg {
		display: block;
		width: 100%;
		height: auto;
	}
	.grid {
		stroke: var(--border-card-hover);
		stroke-width: 1;
	}
	/* 12px in the drawing: still 11px once the chart shrinks to a phone. */
	.tick,
	.axis,
	.ghost-label {
		fill: var(--text-muted);
		font-size: 12px;
		font-family: var(--font-family);
	}
	.ghost {
		fill: none;
		stroke: var(--text-muted);
		stroke-width: 1.2;
		stroke-dasharray: 3 4;
		opacity: 0.7;
	}
	.main {
		fill: none;
		stroke: var(--accent-hover);
		stroke-width: 2.5;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.drop {
		stroke: var(--text-muted);
		stroke-dasharray: 2 3;
	}
	.dot {
		fill: var(--tone);
		stroke: var(--bg-primary);
		stroke-width: 2;
		transition: fill var(--transition-normal);
	}
	.halo {
		fill: var(--tone);
		opacity: 0.25;
		transform-box: fill-box;
		transform-origin: center;
		animation: pulse 2.4s ease-in-out infinite;
	}
	@keyframes pulse {
		0%,
		100% {
			transform: scale(0.7);
			opacity: 0.35;
		}
		50% {
			transform: scale(1.3);
			opacity: 0.08;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.halo {
			animation: none;
		}
	}
	@media (max-width: 720px) {
		.explorer {
			grid-template-columns: minmax(0, 1fr);
			padding: 1rem;
		}
		.chart {
			order: -1;
		}
	}
</style>
