<script lang="ts">
	// About / methodology: what tartuffe.me is, how facts, stances and
	// contradictions are built, how the score is computed (with a live chart),
	// the limits, the neutrality rules and what the pre-poll is and is not.
	import { lang } from '$lib/i18n';
	import Pipeline from '$lib/components/about/Pipeline.svelte';
	import ContradictionFlow from '$lib/components/about/ContradictionFlow.svelte';
	import BundleDiagram from '$lib/components/about/BundleDiagram.svelte';
	import ScoreExplorer from '$lib/components/about/ScoreExplorer.svelte';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	// Fades a section in when it scrolls into view. Without JS, or with
	// reduced motion, the section is simply shown.
	function reveal(node: HTMLElement) {
		if (typeof IntersectionObserver === 'undefined') return;
		if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
		node.classList.add("about-reveal");
		const io = new IntersectionObserver(
			(entries) => {
				for (const e of entries) {
					if (e.isIntersecting) {
						node.classList.add('shown');
						io.disconnect();
					}
				}
			},
			{ rootMargin: '0px 0px -10% 0px' }
		);
		io.observe(node);
		return { destroy: () => io.disconnect() };
	}

	const content = {
		fr: {
			metaTitle: 'À propos — tartuffe.me',
			metaDesc:
				'Comment tartuffe.me collecte les votes et déclarations des responsables politiques, détecte leurs contradictions et calcule un score de cohérence. Méthode, limites et neutralité.',
			kicker: 'À propos',
			title: 'Ils disent. Ils votent. On compare.',
			lead: 'La cohérence, pas la vérité : on ne dit pas qui a raison, on montre qui se contredit. Preuves à l’appui.',
			heroPills: ['100 % sourcé', 'Mêmes règles pour tous', 'Corrigé par vous'],

			howKicker: 'Méthode',
			howTitle: 'Comment ça marche',
			howLead: 'Des sources publiques au score. Automatique, identique pour tous.',

			sortTitle: 'Une vraie contradiction, pas une nuance',
			sortBody: [
				'Préciser sa pensée n’est pas se contredire. On ne retient que deux faits qui ne peuvent pas être vrais en même temps.',
				'Les deux faits côte à côte, datés, cités, sourcés.'
			],

			scoreKicker: 'Le score',
			scoreTitle: 'Un score sur 100',
			scoreLead: 'On part de 100. Chaque contradiction distincte fait baisser le score.',
			formulaLabel: 'La formule',
			defs: [
				['F', 'le nombre de faits connus sur la personne'],
				['B', 'le nombre de contradictions distinctes : les paires d’un même sujet forment un « lot » et ne comptent qu’une fois'],
				['E', 'les paires en plus à l’intérieur des lots (nombre total de paires − B), qui pèsent très peu']
			],
			bundleNote:
				'Trois faits qui se contredisent sur les retraites forment trois paires, mais un seul lot : on ne compte pas trois fois le même revirement.',
			rules: [
				['100', 'pour une personne sans aucun fait : il n’y a rien à reprocher, mais rien à juger non plus.'],
				['≈ 95', 'avec une contradiction.'],
				['75', 'faits au minimum dans le calcul : une contradiction parmi très peu de faits n’écrase pas le score.'],
				['½ + ½', 'pour un parti : la moitié vient de son propre score, l’autre moitié de la moyenne de ses membres.']
			],
			tryTitle: 'Essayez',
			readTitle: 'Comment le lire',
			readLow: 'Un score bas',
			readLowBody: ': beaucoup de contradictions pour ce que l’on sait.',
			readHigh: 'Un score élevé avec peu de faits',
			readHighBody: ': on en sait peu, pas « honnête ». Regardez le nombre de faits.',

			limitsKicker: 'Fiabilité',
			limitsTitle: 'L’IA détecte, vous corrigez',
			limits: [
				['Tout est sourcé.', 'Date, citation exacte, lien vers l’original.'],
				['Vérifié deux fois.', 'Chaque contradiction est relue avant d’être publiée, puis auditée régulièrement.'],
				['Vous avez le dernier mot.', 'Un fait ou une contradiction faux ? Signalez-le : s’il est fondé, il disparaît.'],
				['Corrections publiques.', 'Chaque retrait est affiché, avec sa raison.']
			],
			correctionsLink: 'Voir les corrections',

			neutralKicker: 'Neutralité',
			neutralTitle: 'Gauche, droite : même traitement',
			neutral: [
				['Même code pour tous', 'Mêmes règles pour chaque élu et chaque parti.'],
				['Zéro intervention', 'Personne n’est noté à la main.'],
				['Zéro argent politique', 'Aucun parti, aucun candidat, aucune pub.'],
				['Indépendant', 'Un projet collaboratif, porté par sa communauté.']
			],

			pollKicker: 'Pré-sondage',
			pollTitle: 'Présidentielle 2027 : votez, comparez',
			pollBody: [
				'Votre choix, face au score de chaque candidat.',
				'Un vote par citoyen français majeur, prouvé par ZKPassport. Aucune autre donnée transmise.',
				'Consultation ouverte, pas un sondage au sens de la loi du 19 juillet 1977. Résultats masqués sous un seuil de votants et pendant le silence électoral.'
			],
			pollLink: 'Aller au pré-sondage',
			verifLink: 'Comment marche la vérification'
		},
		en: {
			metaTitle: 'About — tartuffe.me',
			metaDesc:
				'How tartuffe.me collects the votes and statements of politicians, detects their contradictions and computes a consistency score. Method, limits and neutrality.',
			kicker: 'About',
			title: 'They say. They vote. We compare.',
			lead: 'Consistency, not truth: we don’t say who is right, we show who contradicts themselves. With the proof.',
			heroPills: ['100 % sourced', 'Same rules for all', 'Corrected by you'],

			howKicker: 'Method',
			howTitle: 'How it works',
			howLead: 'From public sources to the score. Automatic, the same for all.',

			sortTitle: 'A real contradiction, not a nuance',
			sortBody: [
				'Clarifying is not contradicting. We keep only two facts that cannot both be true.',
				'Both facts side by side, dated, quoted, sourced.'
			],

			scoreKicker: 'The score',
			scoreTitle: 'A score out of 100',
			scoreLead: 'It starts at 100. Each distinct contradiction lowers it.',
			formulaLabel: 'The formula',
			defs: [
				['F', 'the number of known facts about the person'],
				['B', 'the number of distinct contradictions: the pairs on one topic form a “bundle” and count once'],
				['E', 'the extra pairs inside bundles (total pairs − B), which weigh very little']
			],
			bundleNote:
				'Three facts that contradict each other on pensions form three pairs, but a single bundle: the same U-turn is not counted three times.',
			rules: [
				['100', 'for a person with no facts at all: nothing to hold against them, but nothing to judge either.'],
				['≈ 95', 'with one contradiction.'],
				['75', 'facts at least in the computation: one contradiction among very few facts does not crush the score.'],
				['½ + ½', 'for a party: half comes from its own score, half from the average of its members.']
			],
			tryTitle: 'Try it',
			readTitle: 'How to read it',
			readLow: 'A low score',
			readLowBody: ': many contradictions for what we know.',
			readHigh: 'A high score with few facts',
			readHighBody: ': we know little, not “honest”. Check the number of facts.',

			limitsKicker: 'Reliability',
			limitsTitle: 'The AI detects, you correct',
			limits: [
				['Everything is sourced.', 'Date, exact quote, link to the original.'],
				['Checked twice.', 'Each contradiction is reread before it is published, then audited regularly.'],
				['You have the last word.', 'A wrong fact or contradiction? Report it: if you are right, it goes.'],
				['Public corrections.', 'Every removal is shown, with its reason.']
			],
			correctionsLink: 'See the corrections',

			neutralKicker: 'Neutrality',
			neutralTitle: 'Left or right: same treatment',
			neutral: [
				['Same code for all', 'Same rules for every politician and every party.'],
				['Zero intervention', 'Nobody is scored by hand.'],
				['Zero political money', 'No party, no candidate, no ads.'],
				['Independent', 'A collaborative project, driven by its community.']
			],

			pollKicker: 'Pre-poll',
			pollTitle: '2027 presidential: vote, compare',
			pollBody: [
				'Your choice, next to each candidate’s score.',
				'One vote per adult French citizen, proved with ZKPassport. No other data sent.',
				'An open consultation, not a poll under the French law of 19 July 1977. Results hidden below a voter threshold and during the electoral blackout.'
			],
			pollLink: 'Go to the pre-poll',
			verifLink: 'How the verification works'
		}
	};

	let c = $derived(content[currentLang]);
</script>

<svelte:head>
	<title>{c.metaTitle}</title>
	<meta name="description" content={c.metaDesc} />
</svelte:head>

{#snippet neutralIcon(i: number)}
	{#if i === 0}
		<path d="M12 4v16M5 8h14M5 8l-3 6a3 3 0 0 0 6 0zM19 8l-3 6a3 3 0 0 0 6 0zM8 20h8" />
	{:else if i === 1}
		<path d="M7 11V6.5a1.5 1.5 0 0 1 3 0V11M10 10V5a1.5 1.5 0 0 1 3 0v5M13 10V6a1.5 1.5 0 0 1 3 0v6M7 11a1.5 1.5 0 0 0-3 0v2a7 7 0 0 0 7 7h1a6 6 0 0 0 6-6v-2M4 4l16 16" />
	{:else if i === 2}
		<circle cx="12" cy="12" r="8" /><path d="M14.5 9a3 3 0 1 0 0 6M8 11h5M8 13h5M6.3 6.3l11.4 11.4" />
	{:else}
		<circle cx="8" cy="9" r="2.6" /><circle cx="16" cy="9" r="2.6" /><path d="M3.5 19a4.5 4.5 0 0 1 9 0M11.5 19a4.5 4.5 0 0 1 9 0" />
	{/if}
{/snippet}

<article class="page">
	<!-- Hero -->
	<header class="hero">
		<figure class="mirror" aria-hidden="true">
			<svg viewBox="0 0 200 240">
				<defs>
					<radialGradient id="about-glass" cx="50%" cy="38%" r="65%">
						<stop offset="0%" stop-color="var(--accent)" stop-opacity="0.28" />
						<stop offset="70%" stop-color="var(--bg-secondary)" stop-opacity="0.9" />
						<stop offset="100%" stop-color="var(--bg-primary)" stop-opacity="1" />
					</radialGradient>
					<linearGradient id="about-sheen" x1="0" x2="1" y1="0" y2="0">
						<stop offset="0%" stop-color="var(--text-primary)" stop-opacity="0" />
						<stop offset="50%" stop-color="var(--text-primary)" stop-opacity="0.16" />
						<stop offset="100%" stop-color="var(--text-primary)" stop-opacity="0" />
					</linearGradient>
					<filter id="about-blur" x="-30%" y="-30%" width="160%" height="160%"><feGaussianBlur stdDeviation="10" /></filter>
					<clipPath id="about-oval"><ellipse cx="100" cy="112" rx="66" ry="88" /></clipPath>
				</defs>
				<ellipse class="halo" filter="url(#about-blur)" cx="100" cy="112" rx="88" ry="108" />
				<ellipse cx="100" cy="112" rx="66" ry="88" fill="url(#about-glass)" />
				<g clip-path="url(#about-oval)">
					<rect class="sheen" x="-60" y="0" width="50" height="240" fill="url(#about-sheen)" transform="skewX(-18)" />
					<!-- Facts floating in the glass, two of them linked by a contradiction. -->
					<line class="clash" x1="74" y1="96" x2="128" y2="132" />
					<circle class="mote m1" cx="74" cy="96" r="4" />
					<circle class="mote m2" cx="128" cy="132" r="4" />
					<circle class="mote dim m3" cx="112" cy="70" r="2.5" />
					<circle class="mote dim m4" cx="86" cy="152" r="2.5" />
					<circle class="mote dim m5" cx="132" cy="92" r="2" />
					<circle class="mote dim m6" cx="66" cy="128" r="2" />
				</g>
				<ellipse class="frame" cx="100" cy="112" rx="66" ry="88" />
				<ellipse class="frame-outer" cx="100" cy="112" rx="74" ry="96" />
				<path class="frame" d="M100 16 l6 -8 l-6 -6 l-6 6z" />
			</svg>
		</figure>
		<span class="kicker">{c.kicker}</span>
		<h1>{c.title}</h1>
		<p class="lead">{c.lead}</p>
		<ul class="pills">
			{#each c.heroPills as p}<li>{p}</li>{/each}
		</ul>
	</header>

	<!-- How it works -->
	<section id="how" use:reveal>
		<span class="kicker">{c.howKicker}</span>
		<h2>{c.howTitle}</h2>
		<p class="section-lead">{c.howLead}</p>
		<Pipeline lang={currentLang} />
	</section>

	<section class="card sort" use:reveal>
		<div class="sort-text">
			<h3>{c.sortTitle}</h3>
			{#each c.sortBody as p}<p>{p}</p>{/each}
		</div>
		<div class="sort-figure"><ContradictionFlow lang={currentLang} /></div>
	</section>

	<!-- Score -->
	<section id="score" use:reveal>
		<span class="kicker">{c.scoreKicker}</span>
		<h2>{c.scoreTitle}</h2>
		<p class="section-lead">{c.scoreLead}</p>

		<div class="formula-box">
			<span class="formula-label">{c.formulaLabel}</span>
			<p class="formula">
				<span class="fx">score</span> = 100 × exp(
				<span class="frac"><span>−1{currentLang === 'fr' ? ',' : '.'}5 × <b>B</b></span><span>max(<b>F</b>, 75)</span></span>
				− 0{currentLang === 'fr' ? ',' : '.'}03 × <b>B</b> − 0{currentLang === 'fr' ? ',' : '.'}05 × ln(1 + <b>E</b>) )
			</p>
			<dl class="defs">
				{#each c.defs as [k, v]}
					<div><dt>{k}</dt><dd>{v}</dd></div>
				{/each}
			</dl>
		</div>

		<div class="bundle-row">
			<BundleDiagram lang={currentLang} />
			<p class="bundle-note">{c.bundleNote}</p>
		</div>

		<ul class="rules">
			{#each c.rules as [n, t]}
				<li><span class="rule-n">{n}</span><span>{t}</span></li>
			{/each}
		</ul>

		<h3 class="try">{c.tryTitle}</h3>
		<ScoreExplorer lang={currentLang} />

		<div class="read">
			<h3>{c.readTitle}</h3>
			<div class="read-grid">
				<p class="read-low"><strong>{c.readLow}</strong> {c.readLowBody}</p>
				<p class="read-high"><strong>{c.readHigh}</strong> {c.readHighBody}</p>
			</div>
		</div>
	</section>

	<!-- Limits -->
	<section id="trust" use:reveal>
		<span class="kicker">{c.limitsKicker}</span>
		<h2>{c.limitsTitle}</h2>
		<ol class="limits">
			{#each c.limits as [head, body]}
				<li><strong>{head}</strong> {body}</li>
			{/each}
		</ol>
		<p class="more"><a href="/corrections">{c.correctionsLink} →</a></p>
	</section>

	<!-- Neutrality -->
	<section id="neutrality" use:reveal>
		<span class="kicker">{c.neutralKicker}</span>
		<h2>{c.neutralTitle}</h2>
		<div class="neutral">
			{#each c.neutral as [head, body], i}
				<div class="card neutral-item">
					<svg viewBox="0 0 24 24" aria-hidden="true">{@render neutralIcon(i)}</svg>
					<h3>{head}</h3>
					<p>{body}</p>
				</div>
			{/each}
		</div>
	</section>

	<!-- Pre-poll -->
	<section id="poll" class="card poll" use:reveal>
		<span class="kicker">{c.pollKicker}</span>
		<h2>{c.pollTitle}</h2>
		{#each c.pollBody as p}<p>{p}</p>{/each}
		<div class="poll-links">
			<a class="cta" href="/election/presidentielle-2027">{c.pollLink}</a>
			<a href="/verification">{c.verifLink} →</a>
		</div>
	</section>
</article>

<style>
	.page {
		max-width: 46rem;
		margin: 0 auto;
		overflow-x: clip;
	}
	.hero {
		text-align: center;
		margin: 0.5rem 0 3.5rem;
	}
	.mirror {
		width: 9.5rem;
		margin: 0 auto 1rem;
	}
	.mirror svg {
		display: block;
		width: 100%;
		height: auto;
	}
	.halo {
		fill: var(--accent-glow);
		opacity: 0.35;
		transform-box: fill-box;
		animation: breathe 6s ease-in-out infinite;
		transform-origin: center;
	}
	.frame {
		fill: none;
		stroke: var(--accent-hover);
		stroke-width: 2;
	}
	.frame-outer {
		fill: none;
		stroke: var(--border-card-hover);
		stroke-width: 1;
	}
	.sheen {
		animation: sweep 7s ease-in-out infinite;
	}
	.mote {
		fill: var(--text-primary);
		opacity: 0.85;
		animation: float 8s ease-in-out infinite;
	}
	.mote.dim {
		opacity: 0.35;
	}
	.m2 {
		animation-delay: -2s;
	}
	.m3 {
		animation-delay: -4s;
	}
	.m4 {
		animation-delay: -1s;
	}
	.m5 {
		animation-delay: -5s;
	}
	.m6 {
		animation-delay: -3s;
	}
	.clash {
		stroke: var(--red);
		stroke-width: 1.5;
		stroke-dasharray: 3 3;
		opacity: 0.8;
	}
	@keyframes sweep {
		0%,
		15% {
			transform: skewX(-18deg) translateX(0);
		}
		60%,
		100% {
			transform: skewX(-18deg) translateX(300px);
		}
	}
	@keyframes float {
		0%,
		100% {
			transform: translateY(0);
		}
		50% {
			transform: translateY(-4px);
		}
	}
	@keyframes breathe {
		0%,
		100% {
			opacity: 0.25;
			transform: scale(0.97);
		}
		50% {
			opacity: 0.45;
			transform: scale(1.02);
		}
	}

	.kicker {
		display: block;
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.14em;
		text-transform: uppercase;
		color: var(--accent-hover);
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(2rem, 6.5vw, 3.1rem);
		font-weight: 600;
		line-height: 1.08;
		margin: 0.35rem auto 1rem;
		max-width: 36rem;
		text-wrap: balance;
	}
	.lead {
		font-size: 1.08rem;
		color: var(--text-secondary);
		line-height: 1.6;
		max-width: 38rem;
		margin: 0 auto 1.25rem;
	}
	.pills {
		list-style: none;
		padding: 0;
		margin: 0;
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		gap: 0.45rem;
	}
	.pills li {
		font-size: 0.78rem;
		padding: 0.25rem 0.75rem;
		border-radius: var(--radius-full);
		border: 1px solid var(--glass-border);
		color: var(--text-secondary);
		background: var(--bg-card);
	}

	section {
		margin-bottom: 3.5rem;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: clamp(1.6rem, 4.5vw, 2.1rem);
		font-weight: 600;
		line-height: 1.15;
		margin: 0.25rem 0 0.6rem;
		text-wrap: balance;
	}
	h3 {
		font-family: var(--font-serif);
		font-size: 1.3rem;
		font-weight: 600;
		margin: 0 0 0.5rem;
	}
	p,
	li,
	dd {
		color: var(--text-secondary);
		font-size: 0.95rem;
		line-height: 1.6;
	}
	p {
		margin: 0 0 0.75rem;
	}
	strong {
		color: var(--text-primary);
		font-weight: 600;
	}
	.section-lead {
		font-size: 1.02rem;
		margin-bottom: 1.5rem;
	}
	.card {
		padding: 1.4rem 1.5rem;
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		border: 1px solid var(--border-card);
	}

	/* Contradiction sorting */
	.sort {
		display: grid;
		grid-template-columns: minmax(0, 1fr) minmax(0, 1.15fr);
		gap: 1.5rem;
		align-items: center;
		margin-top: -1.5rem;
	}

	/* Formula */
	.formula-box {
		padding: 1.25rem 1.4rem;
		border-radius: var(--radius-lg);
		border: 1px solid var(--border-card-hover);
		background: linear-gradient(135deg, color-mix(in srgb, var(--accent) 10%, transparent), transparent 60%), var(--bg-card);
		margin-bottom: 1.75rem;
	}
	.formula-label {
		font-size: 0.72rem;
		letter-spacing: 0.12em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
	.formula {
		font-family: var(--font-serif);
		font-variant-numeric: lining-nums;
		font-size: clamp(1rem, 3.4vw, 1.3rem);
		color: var(--text-primary);
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		justify-content: center;
		gap: 0.35rem;
		margin: 0.6rem 0 1rem;
		text-align: center;
	}
	.fx {
		font-style: italic;
	}
	.formula b {
		color: var(--accent-hover);
		font-weight: 700;
	}
	.frac {
		display: inline-flex;
		flex-direction: column;
		align-items: center;
		line-height: 1.25;
	}
	.frac span:first-child {
		border-bottom: 1px solid var(--text-secondary);
		padding: 0 0.25rem 0.1rem;
	}
	.frac span:last-child {
		padding-top: 0.1rem;
	}
	.defs {
		margin: 0;
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
	}
	.defs div {
		display: grid;
		grid-template-columns: 1.6rem minmax(0, 1fr);
		gap: 0.5rem;
	}
	dt {
		font-family: var(--font-serif);
		font-weight: 700;
		font-size: 1.1rem;
		color: var(--accent-hover);
		line-height: 1.4;
	}
	dd {
		margin: 0;
	}

	.bundle-row {
		display: grid;
		grid-template-columns: minmax(0, 1.2fr) minmax(0, 1fr);
		gap: 1.25rem;
		align-items: center;
		margin-bottom: 1.75rem;
	}
	.bundle-note {
		margin: 0;
		font-style: italic;
	}

	.rules {
		list-style: none;
		padding: 0;
		margin: 0 0 2.25rem;
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.75rem;
	}
	.rules li {
		display: flex;
		gap: 0.75rem;
		align-items: baseline;
		padding: 0.85rem 1rem;
		border-radius: var(--radius-md);
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		font-size: 0.9rem;
	}
	.rule-n {
		font-family: var(--font-serif);
		font-variant-numeric: lining-nums;
		font-size: 1.5rem;
		font-weight: 600;
		color: var(--text-primary);
		white-space: nowrap;
		min-width: 3.2rem;
	}
	.try {
		margin-bottom: 0.75rem;
	}

	.read {
		margin-top: 1.75rem;
	}
	.read-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.75rem;
	}
	.read-grid p {
		margin: 0;
		padding: 1rem 1.1rem;
		border-radius: var(--radius-md);
		background: var(--bg-card);
		border-left: 3px solid var(--border-card-hover);
	}
	.read-low {
		border-left-color: var(--red) !important;
	}
	.read-high {
		border-left-color: var(--amber) !important;
	}

	/* Limits */
	.limits {
		list-style: none;
		padding: 0;
		margin: 0;
		counter-reset: lim;
		display: flex;
		flex-direction: column;
		gap: 0.9rem;
	}
	.limits li {
		counter-increment: lim;
		position: relative;
		padding-left: 2.5rem;
	}
	.limits li::before {
		content: counter(lim);
		position: absolute;
		left: 0;
		top: 0.1rem;
		width: 1.7rem;
		height: 1.7rem;
		display: grid;
		place-items: center;
		border-radius: 50%;
		border: 1px solid var(--amber);
		color: var(--amber);
		font-size: 0.8rem;
		font-weight: 600;
	}
	.more {
		margin-top: 1rem;
	}

	/* Neutrality */
	.neutral {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.9rem;
	}
	.neutral-item {
		padding: 1.2rem 1.25rem;
		transition:
			border-color var(--transition-normal),
			background var(--transition-normal);
	}
	.neutral-item:hover {
		border-color: var(--border-card-hover);
		background: var(--bg-card-hover);
	}
	.neutral-item svg {
		width: 1.6rem;
		height: 1.6rem;
		fill: none;
		stroke: var(--green);
		stroke-width: 1.5;
		stroke-linecap: round;
		stroke-linejoin: round;
		margin-bottom: 0.5rem;
	}
	.neutral-item h3 {
		font-size: 1.15rem;
		margin-bottom: 0.3rem;
	}
	.neutral-item p {
		margin: 0;
		font-size: 0.9rem;
	}

	/* Pre-poll */
	.poll {
		border-color: var(--border-card-hover);
		background: radial-gradient(ellipse 90% 70% at 100% 0%, var(--accent-glow), transparent 60%), var(--bg-card);
	}
	.poll-links {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.75rem 1.25rem;
		margin-top: 1.25rem;
	}
	.cta {
		display: inline-block;
		padding: 0.7rem 1.4rem;
		border-radius: var(--radius-full);
		background: var(--accent);
		color: white;
		font-weight: 600;
		font-size: 0.95rem;
	}
	.cta:hover {
		background: var(--accent-hover);
		color: white;
	}

	/* Scroll reveal (added by the action, so no-JS shows everything). */
	section:global(.about-reveal) {
		opacity: 0;
		transform: translateY(14px);
		transition:
			opacity 0.7s ease,
			transform 0.7s ease;
	}
	section:global(.about-reveal.shown) {
		opacity: 1;
		transform: none;
	}

	@media (max-width: 680px) {
		.sort,
		.bundle-row,
		.rules,
		.read-grid,
		.neutral {
			grid-template-columns: minmax(0, 1fr);
		}
		.sort {
			margin-top: -1rem;
		}
		.card {
			padding: 1.1rem 1rem;
		}
		.formula-box {
			padding: 1rem;
		}
		.hero {
			margin-bottom: 2.75rem;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.halo,
		.sheen,
		.mote {
			animation: none;
		}
		.sheen {
			display: none;
		}
	}
</style>
