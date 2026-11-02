<script lang="ts">
	// An interactive chart of the Tartuffe-Score: the reader picks a number
	// of distinct contradictions (B), of facts (F) and of extra pairs inside
	// the bundles (E), and sees the score, its barometer and its curve over
	// B change live.
	// Tartuffe-Score = 100 · (1 − exp(−1.5·B / max(F, 75) − 0.03·B − 0.05·ln(1 + E)))
	import type { Lang } from '$lib/i18n';
	import TartuffeScore from '$lib/components/TartuffeScore.svelte';

	let { lang }: { lang: Lang } = $props();

	const FLOOR = 75;
	const B_MAX = 40;
	const factOptions = [75, 300, 1000];

	function score(b: number, f: number, e: number): number {
		if (f <= 0) return 0;
		return 100 * (1 - Math.exp((-1.5 * b) / Math.max(f, FLOOR) - 0.03 * b - 0.05 * Math.log(1 + e)));
	}

	let b = $state(1);
	let f = $state(300);
	let e = $state(0);

	let value = $derived(score(b, f, e));
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

	function curve(fv: number, ev: number): string {
		let d = '';
		for (let i = 0; i <= B_MAX; i += 0.5) {
			d += `${i === 0 ? 'M' : 'L'}${x(i).toFixed(1)},${y(score(i, fv, ev)).toFixed(1)}`;
		}
		return d;
	}

	let mainPath = $derived(curve(f, e));
	let areaPath = $derived(`${mainPath}L${x(B_MAX)},${y(0)}L${x(0)},${y(0)}Z`);
	let ghosts = $derived(factOptions.filter((o) => o !== f).map((o) => ({ f: o, d: curve(o, e) })));

	let tone = $derived(value < 20 ? 'var(--green)' : value < 50 ? 'var(--amber)' : 'var(--red)');

	const text = {
		fr: {
			contradictions: 'Contradictions distinctes (B)',
			facts: 'Faits connus (F)',
			extra: 'Paires en plus dans les mêmes lots (E)',
			floor: '≤ 75',
			score: 'Tartuffe-Score',
			axisX: 'contradictions distinctes',
			few: 'Avec si peu de faits, un score bas veut surtout dire que l’on sait encore peu de choses sur cette personne.',
			many: 'Avec beaucoup de faits, chaque contradiction pèse moins : elle est rapportée à tout ce que l’on sait.',
			alt: 'Courbe du Tartuffe-Score en fonction du nombre de contradictions'
		},
		en: {
			contradictions: 'Distinct contradictions (B)',
			facts: 'Known facts (F)',
			extra: 'Extra pairs inside the same bundles (E)',
			floor: '≤ 75',
			score: 'Tartuffe-Score',
			axisX: 'distinct contradictions',
			few: 'With so few facts, a low score mostly means we still know little about this person.',
			many: 'With many facts, each contradiction weighs less: it is measured against everything we know.',
			alt: 'Curve of the Tartuffe-Score as a function of the number of contradictions'
		}
	};
	let c = $derived(text[lang]);
	let dec = $derived(lang === 'fr' ? ',' : '.');
</script>

<div class="explorer">
	<div class="controls">
		<label class="ctl">
			<span class="ctl-head"><span>{c.contradictions}</span><output>{b}</output></span>
			<input type="range" min="0" max={B_MAX} step="1" bind:value={b} />
		</label>

		<div class="ctl">
			<span class="ctl-head"><span id="facts-label">{c.facts}</span></span>
			<div class="toggles" role="radiogroup" aria-labelledby="facts-label">
				{#each factOptions as o}
					<button
						type="button"
						role="radio"
						aria-checked={f === o}
						class:on={f === o}
						onclick={() => (f = o)}>{o === FLOOR ? c.floor : o.toLocaleString(lang)}</button
					>
				{/each}
			</div>
		</div>

		<label class="ctl">
			<span class="ctl-head"><span>{c.extra}</span><output>{e}</output></span>
			<input type="range" min="0" max="30" step="1" bind:value={e} />
		</label>

		<div class="readout" style:--tone={tone}>
			<span class="big">{shown}</span><span class="out">/100</span>
		</div>
		<TartuffeScore honesty={1 - value / 100} {lang} size="md" />
		<p class="formula" aria-hidden="true">
			100 × (1 − exp(−1{dec}5×{b}/{Math.max(f, FLOOR)} − 0{dec}03×{b} − 0{dec}05×ln(1+{e})))
		</p>
		<p class="hint">{f <= FLOOR ? c.few : c.many}</p>
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

			{#each ghosts as g}
				<path class="ghost" d={g.d} />
				<text class="ghost-label" x={x(B_MAX) - 2} y={y(score(B_MAX, g.f, e)) - 4} text-anchor="end"
					>{g.f === FLOOR ? "F ≤ 75" : `F = ${g.f}`}</text
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
	input[type='range'] {
		width: 100%;
		accent-color: var(--accent);
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
	.readout {
		display: flex;
		align-items: baseline;
		gap: 0.2rem;
		margin-top: 0.25rem;
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
		font-size: 0.72rem;
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
	.tick,
	.axis,
	.ghost-label {
		fill: var(--text-muted);
		font-size: 10px;
		font-family: var(--font-family);
	}
	.axis {
		font-size: 10.5px;
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
