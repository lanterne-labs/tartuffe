<script lang="ts">
	// The pipeline of the site, from the sources to the score: a horizontal
	// strip on wide screens (an overview with a flow of data along it), then
	// a timeline with one step per stage, which is all a phone shows.
	// With `steps` (the home page) it is the short version: the same strip,
	// and under it three steps in place of the timeline, each under the
	// stages it covers (sources and facts, stances and contradictions, score).
	import type { Lang } from '$lib/i18n';

	let { lang, steps }: { lang: Lang; steps?: [string, string][] } = $props();

	// The stages each of the three steps covers.
	const GROUPS = [
		[0, 1],
		[2, 3],
		[4]
	];

	type Stage = { key: string; title: string; body: string[] };

	const text: Record<Lang, { alt: string; stages: Stage[] }> = {
		fr: {
			alt: 'Le parcours des données : sources, faits, positions, contradictions, score',
			stages: [
				{
					key: 'sources',
					title: 'Sources',
					body: [
						'Les données ouvertes de l’Assemblée nationale : chaque vote public de chaque député, et les comptes rendus des débats.',
						'Des articles de presse, trouvés par recherche sur le web.',
						'Des messages publiés sur X (Twitter).'
					]
				},
				{
					key: 'facts',
					title: 'Faits',
					body: [
						'Chaque vote, chaque déclaration devient un fait daté, attribué à une personne.',
						'Une décision de justice qui la concerne aussi : une condamnation, un jugement.',
						'Un fait garde toujours le lien vers sa source et la citation exacte : vous pouvez vérifier par vous-même.',
						'Les faits qui disent la même chose sont fusionnés, pour qu’une phrase répétée dix fois ne compte qu’une fois.'
					]
				},
				{
					key: 'stances',
					title: 'Positions',
					body: [
						'Les faits d’une même personne sont regroupés par sujet : ce qu’elle dit et vote sur les retraites, l’énergie, l’école…',
						'Chaque position rassemble ainsi tout ce que l’on sait de cette personne sur ce sujet.'
					]
				},
				{
					key: 'contradictions',
					title: 'Contradictions',
					body: [
						'Une IA compare deux à deux les faits d’une même personne sur un même sujet.',
						'Elle ne retient une contradiction que si les deux faits ne peuvent pas être vrais en même temps. Une nuance ou une précision ne suffit pas.',
						'Une seconde vérification relit chaque contradiction retenue, et l’ensemble est audité régulièrement.'
					]
				},
				{
					key: 'score',
					title: 'Score',
					body: [
						'Le nombre de contradictions, rapporté à tout ce que l’on sait de la personne, donne un score de cohérence sur 100.'
					]
				}
			]
		},
		en: {
			alt: 'The path of the data: sources, facts, stances, contradictions, score',
			stages: [
				{
					key: 'sources',
					title: 'Sources',
					body: [
						'The open data of the Assemblée nationale: every public vote of every deputy, and the transcripts of the debates.',
						'News articles, found by web search.',
						'Posts published on X (Twitter).'
					]
				},
				{
					key: 'facts',
					title: 'Facts',
					body: [
						'Every vote, every statement becomes a dated fact, attributed to a person.',
						'So does a court ruling about them: a conviction, a judgment.',
						'A fact always keeps the link to its source and the exact quote: you can check for yourself.',
						'Facts that say the same thing are merged, so that a sentence repeated ten times counts once.'
					]
				},
				{
					key: 'stances',
					title: 'Stances',
					body: [
						'The facts of one person are grouped by topic: what they say and vote on pensions, energy, schools…',
						'Each stance gathers everything we know about this person on that topic.'
					]
				},
				{
					key: 'contradictions',
					title: 'Contradictions',
					body: [
						'An AI compares, two by two, the facts of one person on one topic.',
						'It keeps a contradiction only when the two facts cannot both be true. A nuance or a clarification is not enough.',
						'A second check re-reads every contradiction kept, and the whole set is audited periodically.'
					]
				},
				{
					key: 'score',
					title: 'Score',
					body: [
						'The number of contradictions, measured against everything we know about the person, gives a consistency score out of 100.'
					]
				}
			]
		}
	};

	let c = $derived(text[lang]);

	// Strip geometry.
	const SW = 700;
	const cx = (i: number) => 70 + i * 140;
</script>

{#snippet icon(key: string)}
	{#if key === 'sources'}
		<path d="M3 20h18M5 10h14M12 3.5 20 8H4zM7 10v7M10.5 10v7M13.5 10v7M17 10v7" />
	{:else if key === 'facts'}
		<path d="M6.5 3h8l4 4v14h-12zM14 3v4.5h4.5M9.5 12.5h6M9.5 16h4" />
	{:else if key === 'stances'}
		<circle cx="8" cy="9" r="3" /><circle cx="16" cy="9" r="3" /><circle cx="12" cy="16" r="3" />
	{:else if key === 'contradictions'}
		<path d="M4 8h14l-3.5-3.5M20 16H6l3.5 3.5" />
	{:else}
		<path d="M4 17a8 8 0 0 1 16 0M12 17l4-5" /><circle cx="12" cy="17" r="1.2" />
	{/if}
{/snippet}

<figure class="strip" class:compact={!!steps} aria-hidden="true">
	<svg viewBox="0 0 {SW} 120">
		<line class="rail" x1={cx(0)} x2={cx(4)} y1="44" y2="44" />
		{#each [0, 1, 2, 3, 4, 5] as p}
			<circle class="particle" cx={cx(0)} cy="44" r="2.6" style:animation-delay="{-p * 1.2}s" />
		{/each}
		{#each c.stages as s, i}
			<g transform="translate({cx(i)} 44)">
				<circle class="node-bg" r="26" class:last={i === 4} />
				<g class="node-icon" transform="translate(-12 -12)">{@render icon(s.key)}</g>
			</g>
			<text class="node-label" x={cx(i)} y="96" text-anchor="middle">{s.title}</text>
		{/each}
	</svg>
</figure>

{#if steps}
	<ol class="groups" aria-label={c.alt}>
		{#each steps as [head, body], g}
			<li style:--span={GROUPS[g].length} style:--i={g}>
				<div class="g-rail" aria-hidden="true">
					{#each GROUPS[g] as i}
						<span class="node" class:last={i === 4}>
							<svg viewBox="0 0 24 24">{@render icon(c.stages[i].key)}</svg>
						</span>
						{#if i < 4}<span class="link"><span class="spark"></span></span>{/if}
					{/each}
				</div>
				<div class="g-text">
					<span class="brace" aria-hidden="true"></span>
					<h3><span class="num">{g + 1}</span>{head}</h3>
					<span class="g-stages">{GROUPS[g].map((i) => c.stages[i].title).join(' · ')}</span>
					<p>{body}</p>
				</div>
			</li>
		{/each}
	</ol>
{:else}
	<ol class="timeline" aria-label={c.alt}>
		{#each c.stages as s, i}
			<li class="step" style:--i={i}>
				<div class="rail-col" aria-hidden="true">
					<span class="node" class:last={i === 4}>
						<svg viewBox="0 0 24 24">{@render icon(s.key)}</svg>
					</span>
					{#if i < c.stages.length - 1}<span class="link"><span class="spark"></span></span>{/if}
				</div>
				<div class="card">
					<h3><span class="num">{i + 1}</span>{s.title}</h3>
					{#each s.body as p}<p>{p}</p>{/each}
				</div>
			</li>
		{/each}
	</ol>
{/if}

<style>
	.strip {
		margin: 0.5rem 0 1.75rem;
	}
	.strip svg {
		display: block;
		width: 100%;
		height: auto;
	}
	.rail {
		stroke: var(--border-card-hover);
		stroke-width: 2;
	}
	.particle {
		fill: var(--accent-hover);
		animation: travel 7.2s linear infinite;
		opacity: 0;
	}
	@keyframes travel {
		0% {
			transform: translateX(0);
			opacity: 0;
		}
		8% {
			opacity: 0.9;
		}
		92% {
			opacity: 0.9;
		}
		100% {
			transform: translateX(560px);
			opacity: 0;
		}
	}
	.node-bg {
		fill: var(--bg-secondary);
		stroke: var(--border-card-hover);
		stroke-width: 1.5;
	}
	.node-bg.last {
		stroke: var(--accent);
	}
	.node-icon,
	.node svg {
		fill: none;
		stroke: var(--text-primary);
		stroke-width: 1.5;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.node-label {
		fill: var(--text-secondary);
		font-family: var(--font-serif);
		font-size: 17px;
		font-weight: 600;
	}

	.timeline {
		list-style: none;
		margin: 0;
		padding: 0;
	}
	.step {
		display: grid;
		grid-template-columns: 2.75rem minmax(0, 1fr);
		gap: 1rem;
	}
	.rail-col {
		display: flex;
		flex-direction: column;
		align-items: center;
	}
	.node {
		width: 2.75rem;
		height: 2.75rem;
		flex-shrink: 0;
		display: grid;
		place-items: center;
		border-radius: 50%;
		background: var(--bg-secondary);
		border: 1px solid var(--border-card-hover);
	}
	.node.last {
		border-color: var(--accent);
		box-shadow: var(--shadow-glow);
	}
	.node svg {
		width: 1.3rem;
		height: 1.3rem;
	}
	.link {
		position: relative;
		flex: 1;
		width: 2px;
		min-height: 1.5rem;
		margin: 0.35rem 0;
		background: var(--border-card-hover);
		overflow: hidden;
		border-radius: 1px;
	}
	.spark {
		position: absolute;
		left: 0;
		width: 2px;
		height: 30%;
		background: linear-gradient(to bottom, transparent, var(--accent-hover), transparent);
		animation: fall 2.8s ease-in-out infinite;
		animation-delay: calc(var(--i) * 0.45s);
	}
	@keyframes fall {
		0% {
			top: -30%;
		}
		100% {
			top: 100%;
		}
	}
	.card {
		padding: 0.15rem 0 1.5rem;
		min-width: 0;
	}
	h3 {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		font-family: var(--font-serif);
		font-size: 1.3rem;
		font-weight: 600;
		margin: 0.45rem 0 0.4rem;
		color: var(--text-primary);
	}
	.num {
		font-family: var(--font-family);
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--accent-hover);
		letter-spacing: 0.08em;
	}
	p {
		color: var(--text-secondary);
		font-size: 0.95rem;
		line-height: 1.6;
		margin: 0 0 0.45rem;
	}

	/* The short version: three steps under the strip, each as wide as the
	   stages it covers (a stage is a fifth of the strip). */
	.strip.compact {
		margin: 0 0 0.4rem;
	}
	.groups {
		list-style: none;
		margin: 0;
		padding: 0;
		display: grid;
		grid-template-columns: repeat(5, minmax(0, 1fr));
	}
	.groups li {
		grid-column: span var(--span);
		padding: 0 0.6rem;
		text-align: center;
	}
	.g-rail,
	.g-stages {
		display: none;
	}
	.brace {
		display: block;
		height: 0.55rem;
		margin: 0 calc(50% / var(--span) - 1.6rem) 0.7rem;
		border: 1px solid var(--border-card-hover);
		border-top: none;
		border-radius: 0 0 0.5rem 0.5rem;
	}
	.groups h3 {
		justify-content: center;
		margin-top: 0;
	}
	.groups p {
		font-size: 0.92rem;
		line-height: 1.5;
		margin: 0;
	}

	@media (max-width: 600px) {
		.strip {
			display: none;
		}
		.groups {
			display: block;
		}
		.groups li {
			display: grid;
			grid-template-columns: 2.75rem minmax(0, 1fr);
			gap: 1rem;
			padding: 0;
			text-align: left;
		}
		.g-rail {
			display: flex;
			flex-direction: column;
			align-items: center;
		}
		.g-rail .link {
			min-height: 1rem;
		}
		.g-text {
			padding: 0.15rem 0 1.5rem;
		}
		.brace {
			display: none;
		}
		.groups h3 {
			justify-content: flex-start;
			margin: 0.45rem 0 0.1rem;
		}
		.g-stages {
			display: block;
			margin-bottom: 0.35rem;
			font-size: 0.75rem;
			letter-spacing: 0.04em;
			color: var(--text-muted);
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.particle,
		.spark {
			animation: none;
		}
		.particle {
			opacity: 0;
		}
		.spark {
			display: none;
		}
	}
</style>
