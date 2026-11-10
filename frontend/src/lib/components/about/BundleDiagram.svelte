<script lang="ts">
	// What B and E count: the contradictory pairs of one topic form one bundle.
	// Three facts that clash on pensions give three pairs but one bundle (B = 1,
	// E = 2); one clash on energy gives one pair, one bundle (B = 1, E = 0).
	import type { Lang } from '$lib/i18n';

	let { lang }: { lang: Lang } = $props();

	const text = {
		fr: {
			alt: 'Schéma : trois faits contradictoires sur un sujet forment un seul lot',
			t1: 'Retraites',
			t2: 'Énergie',
			s1: '3 paires · 1 lot',
			s2: '1 paire · 1 lot',
			total: 'B = 2 lots   ·   E = 4 − 2 = 2'
		},
		en: {
			alt: 'Diagram: three contradictory facts on one topic form a single bundle',
			t1: 'Pensions',
			t2: 'Energy',
			s1: '3 pairs · 1 bundle',
			s2: '1 pair · 1 bundle',
			total: 'B = 2 bundles   ·   E = 4 − 2 = 2'
		}
	};
	let c = $derived(text[lang]);

	const tri = [
		[90, 46],
		[48, 112],
		[132, 112]
	];
	const pair = [
		[240, 84],
		[300, 84]
	];
</script>

<figure class="bundle">
	<svg viewBox="0 0 360 190" role="img" aria-label={c.alt}>
		<ellipse class="hull" cx="90" cy="88" rx="78" ry="56" />
		<ellipse class="hull" cx="270" cy="84" rx="62" ry="34" />
		<text class="topic" x="90" y="24" text-anchor="middle">{c.t1}</text>
		<text class="topic" x="270" y="40" text-anchor="middle">{c.t2}</text>

		<line class="clash" x1={tri[0][0]} y1={tri[0][1]} x2={tri[1][0]} y2={tri[1][1]} />
		<line class="clash" x1={tri[1][0]} y1={tri[1][1]} x2={tri[2][0]} y2={tri[2][1]} style:animation-delay="0.5s" />
		<line class="clash" x1={tri[2][0]} y1={tri[2][1]} x2={tri[0][0]} y2={tri[0][1]} style:animation-delay="1s" />
		<line class="clash" x1={pair[0][0]} y1={pair[0][1]} x2={pair[1][0]} y2={pair[1][1]} style:animation-delay="0.25s" />

		{#each [...tri, ...pair] as [x, y]}
			<circle class="fact" cx={x} cy={y} r="7" />
		{/each}

		<text class="sub" x="90" y="160" text-anchor="middle">{c.s1}</text>
		<text class="sub" x="270" y="136" text-anchor="middle">{c.s2}</text>
		<text class="total" x="180" y="184" text-anchor="middle">{c.total}</text>
	</svg>
</figure>

<style>
	.bundle {
		margin: 0 auto;
		max-width: 26rem;
	}
	svg {
		display: block;
		width: 100%;
		height: auto;
		font-family: var(--font-family);
	}
	.hull {
		fill: color-mix(in srgb, var(--accent) 7%, transparent);
		stroke: var(--border-card-hover);
		stroke-dasharray: 4 4;
	}
	.topic {
		fill: var(--text-primary);
		font-family: var(--font-serif);
		font-size: 15px;
		font-weight: 600;
	}
	.fact {
		fill: var(--bg-secondary);
		stroke: var(--text-secondary);
		stroke-width: 1.5;
	}
	.clash {
		stroke: var(--red);
		stroke-width: 2;
		animation: glow 3s ease-in-out infinite;
	}
	@keyframes glow {
		0%,
		100% {
			opacity: 0.45;
		}
		50% {
			opacity: 1;
		}
	}
	/* 12.5px in the drawing: still 11px once the figure shrinks to a phone. */
	.sub {
		fill: var(--text-secondary);
		font-size: 12.5px;
	}
	.total {
		fill: var(--amber);
		font-size: 12.5px;
		font-weight: 600;
		white-space: pre;
	}
	@media (prefers-reduced-motion: reduce) {
		.clash {
			animation: none;
			opacity: 0.85;
		}
	}
</style>
