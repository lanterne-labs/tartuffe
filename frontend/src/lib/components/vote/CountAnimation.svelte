<script lang="ts">
	// Counting without reading, looping: sealed ballots drop into the count,
	// still sealed; only the result is revealed, out of the opened box.
	// Without motion the last frame shows. A pile of the other voters'
	// ballots and the dim outline of the bars stay in every frame, so no
	// column is ever empty under its label.
	import type { Lang } from '$lib/i18n';

	let { lang }: { lang: Lang } = $props();
	const t = {
		fr: { ballots: 'Bulletins scellés', sum: 'Le comptage', only: 'seul le résultat est révélé', results: 'Résultats' },
		en: { ballots: 'Sealed ballots', sum: 'The count', only: 'only the result is revealed', results: 'Results' }
	};
	let c = $derived(t[lang]);
</script>

<svg viewBox="0 0 340 150" class="anim" role="img" aria-label="{c.ballots} → {c.sum} → {c.results}">
	<!-- The pile of the other ballots, which never leaves. -->
	<g class="pile p1"><rect width="22" height="15" rx="2.5" /><path d="M0 0l11 8 11-8" /></g>
	<g class="pile p2"><rect width="22" height="15" rx="2.5" /><path d="M0 0l11 8 11-8" /></g>
	<text x="54" y="132" text-anchor="middle" class="label">{c.ballots}</text>

	<!-- The sum: a closed box that opens -->
	<g class="box">
		<rect x="140" y="58" width="60" height="44" rx="6" class="body" />
		<g class="lid">
			<rect x="136" y="48" width="68" height="12" rx="4" />
		</g>
	</g>
	<text x="170" y="132" text-anchor="middle" class="label">{c.sum}</text>
	<!-- The ballots, drawn over the box: they drop in through the top. -->
	{#each [0, 1, 2, 3] as i (i)}
		<g class="env e{i}">
			<rect width="22" height="15" rx="2.5" />
			<path d="M0 0l11 8 11-8" />
		</g>
	{/each}

	<text x="170" y="16" text-anchor="middle" class="only">{c.only}</text>

	<!-- The results: the dim outline holds the place, the green grows over it. -->
	<g class="ghost">
		<rect x="262" y="70" width="12" height="32" rx="2" />
		<rect x="280" y="54" width="12" height="48" rx="2" />
		<rect x="298" y="80" width="12" height="22" rx="2" />
	</g>
	<g class="bars">
		<rect x="262" y="70" width="12" height="32" rx="2" class="b1" />
		<rect x="280" y="54" width="12" height="48" rx="2" class="b2" />
		<rect x="298" y="80" width="12" height="22" rx="2" class="b3" />
	</g>
	<text x="286" y="132" text-anchor="middle" class="label">{c.results}</text>
</svg>

<style>
	.anim {
		display: block;
		width: 100%;
		max-width: 26rem;
		height: auto;
		margin: 0 auto;
		font-family: var(--font-family);
	}
	.env rect {
		fill: var(--accent);
	}
	.env path,
	.pile path {
		fill: none;
		stroke: #0a0a0f;
		stroke-width: 1.4;
	}
	.pile rect {
		fill: color-mix(in oklch, var(--accent) 55%, #2a2430);
	}
	.p1 {
		transform: translate(34px, 70px);
	}
	.p2 {
		transform: translate(48px, 82px);
	}
	.ghost rect {
		fill: #3a3038;
	}
	/* 12px in the drawing is about 11px on a phone: the floor of a caption. */
	.label {
		font-size: 12px;
		fill: var(--text-secondary);
	}
	.only {
		font-size: 12px;
		font-weight: 700;
		fill: var(--green);
	}
	.box .body {
		fill: #2a2430;
		stroke: var(--glass-border);
	}
	.lid rect {
		fill: #3a3038;
		stroke: var(--glass-border);
	}
	.lid {
		transform-box: fill-box;
		transform-origin: 0% 100%;
		transform: rotate(-20deg);
	}
	.bars rect {
		fill: var(--green);
		transform-box: fill-box;
		transform-origin: 50% 100%;
	}
	/* The last frame: the ballots in the box, the box open, the results up. */
	.env {
		opacity: 0;
	}
	.e0 {
		transform: translate(26px, 60px);
	}
	.e1 {
		transform: translate(52px, 50px);
	}
	.e2 {
		transform: translate(28px, 88px);
	}
	.e3 {
		transform: translate(56px, 92px);
	}

	@media (prefers-reduced-motion: no-preference) {
		.e0 {
			animation: drop0 7s infinite;
		}
		.e1 {
			animation: drop1 7s infinite;
		}
		.e2 {
			animation: drop2 7s infinite;
		}
		.e3 {
			animation: drop3 7s infinite;
		}
		.lid {
			animation: lid 7s infinite;
		}
		.only {
			animation: show 7s infinite;
		}
		.bars rect {
			animation: grow 7s infinite;
		}
	}
	@keyframes drop0 {
		0%, 4% { opacity: 1; transform: translate(26px, 60px); }
		22% { opacity: 1; transform: translate(159px, 34px); }
		26%, 100% { opacity: 0; transform: translate(159px, 52px); }
	}
	@keyframes drop1 {
		0%, 10% { opacity: 1; transform: translate(52px, 50px); }
		28% { opacity: 1; transform: translate(159px, 34px); }
		32%, 100% { opacity: 0; transform: translate(159px, 52px); }
	}
	@keyframes drop2 {
		0%, 16% { opacity: 1; transform: translate(28px, 88px); }
		34% { opacity: 1; transform: translate(159px, 34px); }
		38%, 100% { opacity: 0; transform: translate(159px, 52px); }
	}
	@keyframes drop3 {
		0%, 22% { opacity: 1; transform: translate(56px, 92px); }
		40% { opacity: 1; transform: translate(159px, 34px); }
		44%, 100% { opacity: 0; transform: translate(159px, 52px); }
	}
	@keyframes lid {
		0%, 46% { transform: rotate(0deg); }
		56%, 92% { transform: rotate(-20deg); }
		100% { transform: rotate(0deg); }
	}
	@keyframes show {
		0%, 50% { opacity: 0; }
		58%, 94% { opacity: 1; }
		100% { opacity: 0; }
	}
	@keyframes grow {
		0%, 60% { transform: scaleY(0); }
		72%, 94% { transform: scaleY(1); }
		100% { transform: scaleY(0); }
	}
</style>
