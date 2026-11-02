<script lang="ts">
	// A half-dial gauge (like a "fear & greed" meter): five bands from red (0)
	// to green (100) and a needle on the score.
	interface Props {
		score: number;
		size?: number;
		label?: string;
	}

	let { score, size = 180, label = '' }: Props = $props();

	const CX = 100;
	const CY = 100;
	const R = 78;
	const BANDS = [
		'var(--red)',
		'color-mix(in oklch, var(--red) 50%, var(--amber))',
		'var(--amber)',
		'color-mix(in oklch, var(--amber) 50%, var(--green))',
		'var(--green)'
	];
	const GAP = 2; // degrees between bands

	function point(deg: number, r: number): [number, number] {
		const a = (deg * Math.PI) / 180;
		return [CX + r * Math.cos(a), CY - r * Math.sin(a)];
	}

	// The arc of band i, from the left (180°) to the right (0°).
	function band(i: number): string {
		const from = 180 - (i * 180) / BANDS.length - (i === 0 ? 0 : GAP / 2);
		const to = 180 - ((i + 1) * 180) / BANDS.length + (i === BANDS.length - 1 ? 0 : GAP / 2);
		const [x1, y1] = point(from, R);
		const [x2, y2] = point(to, R);
		return `M ${x1} ${y1} A ${R} ${R} 0 0 1 ${x2} ${y2}`;
	}

	let clamped = $derived(Math.max(0, Math.min(1, score)));
	// The needle is drawn pointing left (0) and turned clockwise by the score.
	let turn = $derived(clamped * 180);
	let value = $derived(Math.round(clamped * 100));
</script>

<div class="gauge" style="width: {size}px">
	<svg viewBox="0 0 200 136" role="img" aria-label="{label} {value}/100">
		{#each BANDS as color, i}
			<path d={band(i)} stroke={color} stroke-width="16" fill="none" />
		{/each}
		<g class="needle" style="transform: rotate({turn}deg)">
			<line x1={CX} y1={CY} x2={CX - R + 14} y2={CY} />
		</g>
		<circle cx={CX} cy={CY} r="7" class="hub" />
		<text x={CX} y="133" text-anchor="middle" class="value">{value}<tspan class="unit" dx="2">/100</tspan></text>
	</svg>
</div>

<style>
	.gauge {
		max-width: 100%;
	}
	svg {
		display: block;
		width: 100%;
		height: auto;
		overflow: visible;
	}
	.needle {
		transform-origin: 100px 100px;
		transition: transform 700ms cubic-bezier(0.2, 0.8, 0.2, 1);
	}
	.needle line {
		stroke: var(--text-primary);
		stroke-width: 4;
		stroke-linecap: round;
	}
	.hub {
		fill: var(--text-primary);
	}
	.value {
		font-size: 22px;
		font-weight: 700;
		fill: var(--text-primary);
	}
	.unit {
		font-size: 10px;
		font-weight: 600;
		fill: var(--text-muted);
	}
</style>
