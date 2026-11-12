<script lang="ts">
	// About / methodology: what tartuffe.me is, how facts, stances and
	// contradictions are built, how the score is computed (with a live chart),
	// the limits and the neutrality rules. The lab's vote experiment has its
	// own pages (/election/...): one line points there.
	import { eventPaths } from '$lib/event';
	import { LAB_URL, LAB_DOMAIN } from '$lib/contact';
	import { lang } from '$lib/i18n';
	import Pipeline from '$lib/components/about/Pipeline.svelte';
	import ContradictionFlow from '$lib/components/about/ContradictionFlow.svelte';
	import BundleDiagram from '$lib/components/about/BundleDiagram.svelte';
	import ScoreExplorer from '$lib/components/about/ScoreExplorer.svelte';
	import { DECAY, EXTRA_WEIGHT, SMOOTHING, formatConstant } from '$lib/score';
	import BrandMark from '$lib/components/BrandMark.svelte';
	import { onMount } from 'svelte';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	// Fades a section in when it scrolls into view, with a mouse only: on a
	// touch screen a fast flick leaves the sections blank until the observer
	// catches up, and the page is simply shown; the same without JS or with
	// reduced motion. The hidden style applies once the root says so, after
	// the mount, so the server-rendered page never flashes.
	function wantsReveal(): boolean {
		if (typeof IntersectionObserver === 'undefined') return false;
		if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return false;
		if (window.matchMedia('(hover: none)').matches) return false;
		return true;
	}
	let revealOn = $state(false);
	onMount(() => {
		revealOn = wantsReveal();
	});
	function reveal(node: HTMLElement) {
		if (!wantsReveal()) return;
		// A section already on the screen stays as it is: hiding it to fade
		// it in again would be the flash the reveal is meant to avoid.
		if (node.getBoundingClientRect().top < window.innerHeight) return;
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
				'Comment tartuffe.me collecte les votes et déclarations des responsables politiques, détecte leurs contradictions et calcule son Tartuffe-Score. Méthode, limites et neutralité.',
			kicker: 'À propos',
			labLine: 'Tartuffe est le premier produit de Lanterne Labs, un laboratoire indépendant qui conçoit et finance des outils pour une démocratie plus transparente.',
			labLink: 'Lanterne Labs',
			title: 'Cohérent ou tartuffe ?',
			lead: 'Ce qu’ils disent, ce qu’ils votent, ce que la justice a tranché : quand ça ne colle pas, nous le montrons, sources à l’appui. Nous ne jugeons pas qui a raison.',
			heroPills: ['Tout est sourcé', 'Les mêmes règles pour tous', 'Vous pouvez nous corriger'],

			howKicker: 'Méthode',
			howTitle: 'Comment ça marche',
			howLead: 'Des sources publiques jusqu’au score, de façon automatique et identique pour tous.',

			sortTitle: 'Une vraie contradiction, pas une nuance',
			sortBody: [
				'Préciser sa pensée n’est pas se contredire. On ne retient que deux faits qui ne peuvent pas être vrais en même temps.',
				'Les deux faits sont présentés côte à côte, datés, cités et sourcés.'
			],
			courtTitle: 'Les paroles, les actes, et la justice',
			courtBody: [
				'Un élu peut se contredire de trois façons : par une autre déclaration, par un acte (un vote), ou en niant ce que la justice a établi à son sujet.',
				'Une condamnation, une décision de justice ou la conclusion d’une enquête officielle est un fait, comme un vote. Nier ce qu’elle a tranché compte comme une contradiction, même si un recours est en cours.',
				'Une accusation, une mise en examen ou un procès en cours ne comptent pas : seule une décision rendue compte, et seulement sur le point précis qu’elle tranche.'
			],

			scoreKicker: 'Le Tartuffe-Score',
			scoreTitle: 'De cohérent à tartuffe',
			scoreLead: 'On part de 0, tout à gauche du baromètre. Chaque contradiction distincte pousse le repère vers « tartuffe ». Qui parle beaucoup finit par se contredire un peu : les contradictions d’une personne sont donc comparées à celles que l’on attendrait de ses faits, au taux moyen des candidats.',
			formulaLabel: 'La formule',
			defs: [
				['B', 'le nombre de contradictions distinctes : les paires d’un même sujet forment un « lot » et ne comptent qu’une fois'],
				['E', 'les paires en plus à l’intérieur des lots (nombre total de paires − B), qui pèsent très peu'],
				['A', 'les contradictions attendues : le nombre de faits connus sur la personne × le taux moyen des candidats (leurs contradictions distinctes divisées par leurs faits, mesuré en continu)']
			],
			bundleNote:
				'Trois faits qui se contredisent sur les retraites forment trois paires, mais un seul lot : on ne compte pas trois fois le même revirement.',
			rules: [
				['< 10', 'faits : pas de repère, il n’y a pas encore assez à juger.'],
				['≤ 18', 'pour une seule contradiction : 18 avec peu de faits, moins à mesure que l’on en connaît davantage.'],
				['≈ 45', 'pour qui se contredit autant que la moyenne des candidats, une fois beaucoup de faits connus ; 70 pour le double de la moyenne. Plus de faits avec le même taux de contradictions ne changent pas le score.'],
				['🚧', 'score en cours de consolidation : la détection manque des contradictions bien plus qu’elle n’en invente, donc un score est bien plus souvent trop bas que trop haut. Un score compte dans les classements quand la personne a assez de faits pour qu’une feuille blanche veuille dire quelque chose (le seuil suit la moyenne des candidats), ou dès qu’il dépasse le plus bas score d’un candidat qui y est déjà : avec si peu de faits c’est le nombre de ses contradictions qui l’y a mis, et plus de faits en apportent d’autres.'],
				['+ 3', 'contradictions ajoutées aux attendues : avec peu de faits on n’en attend presque aucune, et c’est alors leur nombre qui compte, pas leur taux.'],
				['½ + ½', 'pour un parti : la moitié vient de son propre score, l’autre moitié de la moyenne de ses membres.']
			],
			tryTitle: 'Essayez',
			readTitle: 'Comment le lire',
			readLow: 'Un repère vers « tartuffe »',
			readLowBody: ': beaucoup de contradictions pour ce que l’on sait.',
			readHigh: 'Un repère vers « cohérent » avec peu de faits',
			readHighBody: ' : on en sait peu, ce qui ne veut pas dire « cohérent ». Regardez le nombre de faits.',

			limitsKicker: 'Fiabilité',
			limitsTitle: 'L’IA détecte, vous corrigez',
			limits: [
				['Tout est sourcé.', 'Date, citation exacte, lien vers l’original.'],
				['Vérifié deux fois, par l’IA.', 'Chaque contradiction est relue par une seconde IA avant publication, et nous en contrôlons régulièrement un échantillon. L’IA peut se tromper : signalez-nous les erreurs.'],
				['Vous avez le dernier mot.', 'Un fait ou une contradiction vous semble faux ? Signalez-le : si vous avez raison, il est retiré.'],
				['Corrections publiques.', 'Chaque retrait est affiché, avec sa raison.']
			],
			correctionsLink: 'Voir les corrections',

			privacyKicker: 'Confidentialité',
			privacyTitle: 'Vous restez anonyme',
			privacy: [
				['Sans e-mail.', 'Google ne nous transmet qu’un identifiant anonyme.'],
				['Votre identité reste à vous.', 'Votre passeport prouve « français et majeur » sans quitter votre téléphone.'],
				['Sans traceur.', 'Sans publicité, sans cookie, sans adresse IP conservée ;  une mesure d’audience anonyme, hébergée par nous.']
			],
			privacyLink: 'Tout le détail',

			neutralKicker: 'Neutralité',
			neutralTitle: 'Gauche, droite : même traitement',
			neutral: [
				['Le même code pour tous', 'Les mêmes règles pour chaque élu et chaque parti.'],
				['Aucune note à la main', 'Aucun élu n’est noté manuellement.'],
				['Aucun argent politique', 'Ni parti, ni candidat, ni publicité.'],
				['Indépendant', 'Un projet collaboratif, porté par sa communauté.']
			],

			pollLink: 'Lanterne Labs mène aussi, à part, une expérience de vote secret en ligne : la présidentielle 2027'
		},
		en: {
			metaTitle: 'About — tartuffe.me',
			metaDesc:
				'How tartuffe.me collects the votes and statements of politicians, detects their contradictions and computes its Tartuffe-Score. Method, limits and neutrality.',
			kicker: 'About',
			labLine: 'Tartuffe is the first product of Lanterne Labs, an independent lab that builds and funds tools for more transparent democracies.',
			labLink: 'Lanterne Labs',
			title: 'Coherent or tartuffe?',
			lead: 'What they say, how they vote, what the courts ruled: when it does not add up, we show it, with the sources. We don’t say who is right.',
			heroPills: ['100 % sourced', 'Same rules for all', 'Corrected by you'],

			howKicker: 'Method',
			howTitle: 'How it works',
			howLead: 'From public sources to the score. Automatic, the same for all.',

			sortTitle: 'A real contradiction, not a nuance',
			sortBody: [
				'Clarifying is not contradicting. We keep only two facts that cannot both be true.',
				'Both facts side by side, dated, quoted, sourced.'
			],
			courtTitle: 'Words, deeds, and the courts',
			courtBody: [
				'A politician can contradict themselves in three ways: by another statement, by a deed (a vote), or by denying what a court has established about them.',
				'A conviction, a court ruling or the conclusion of an official inquiry is a fact, like a vote. Denying what it settled counts as a contradiction, even while an appeal is pending.',
				'An accusation, a charge or a trial under way does not count: only a ruling handed down does, and only on the precise point it settles.'
			],

			scoreKicker: 'The Tartuffe-Score',
			scoreTitle: 'From coherent to tartuffe',
			scoreLead: 'It starts at 0, at the left end of the barometer. Each distinct contradiction pushes the marker towards “tartuffe”. Whoever speaks a lot ends up contradicting themselves a little: so a person’s contradictions are compared with those expected of their facts, at the candidates’ average rate.',
			formulaLabel: 'The formula',
			defs: [
				['B', 'the number of distinct contradictions: the pairs on one topic form a “bundle” and count once'],
				['E', 'the extra pairs inside bundles (total pairs − B), which weigh very little'],
				['A', 'the expected contradictions: the number of known facts about the person × the candidates’ average rate (their distinct contradictions divided by their facts, measured continuously)']
			],
			bundleNote:
				'Three facts that contradict each other on pensions form three pairs, but a single bundle: the same U-turn is not counted three times.',
			rules: [
				['< 10', 'facts: no marker, not enough to judge yet.'],
				['≤ 18', 'with one contradiction: 18 with few facts, less as more of them are known.'],
				['≈ 45', 'for someone who contradicts themselves as much as the candidates’ average, once many facts are known; 70 at twice the average. More facts at the same rate of contradictions do not change the score.'],
				['🚧', 'score being consolidated: detection misses contradictions far more than it invents them, so a score is far more often too low than too high. A score counts in the rankings once the person has enough facts for a clean sheet to mean something (the threshold follows the candidates’ average), or as soon as it passes the lowest score of a candidate already ranked: with so few facts it is the count of their contradictions that put them there, and more facts bring more.'],
				['+ 3', 'contradictions added to the expected ones: with few facts almost none is expected, and their count then decides, not their rate.'],
				['½ + ½', 'for a party: half comes from its own score, half from the average of its members.']
			],
			tryTitle: 'Try it',
			readTitle: 'How to read it',
			readLow: 'A marker towards “tartuffe”',
			readLowBody: ': many contradictions for what we know.',
			readHigh: 'A marker towards “coherent” with few facts',
			readHighBody: ': we know little, not “coherent”. Check the number of facts.',

			limitsKicker: 'Reliability',
			limitsTitle: 'The AI detects, you correct',
			limits: [
				['Everything is sourced.', 'Date, exact quote, link to the original.'],
				['Checked twice, by AI.', 'Each contradiction is checked by a second AI before it is published, and we review a sample regularly. AI can be wrong: tell us about errors.'],
				['You have the last word.', 'A wrong fact or contradiction? Report it: if you are right, it goes.'],
				['Public corrections.', 'Every removal is shown, with its reason.']
			],
			correctionsLink: 'See the corrections',

			privacyKicker: 'Privacy',
			privacyTitle: 'You stay anonymous',
			privacy: [
				['E-mail-free.', 'Google gives us only an anonymous identifier.'],
				['Your identity stays yours.', 'Your passport proves “French, adult” without leaving your phone.'],
				['Tracker-free.', 'Without ads, cookies or a kept IP address; an anonymous audience measure, hosted by us.']
			],
			privacyLink: 'All the details',

			neutralKicker: 'Neutrality',
			neutralTitle: 'Left or right: same treatment',
			neutral: [
				['Same code for all', 'Same rules for every politician and every party.'],
				['Zero intervention', 'Nobody is scored by hand.'],
				['Zero political money', 'No party, no candidate, no ads.'],
				['Independent', 'A collaborative project, driven by its community.']
			],

			pollLink: 'Lanterne Labs also runs, apart, an experiment in private online voting: the 2027 presidential election'
		}
	};

	let c = $derived(content[currentLang]);
</script>

<svelte:head>
	<title>{c.metaTitle}</title>
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

<article class="page" class:reveal-on={revealOn}>
	<!-- Hero -->
	<header class="hero">
		<BrandMark size="hero" />
		<span class="kicker">{c.kicker}</span>
		<h1>{c.title}</h1>
		<p class="lead">{c.lead}</p>
		<p class="lab-line">{c.labLine} <a href="/lab">{c.labLink} →</a> · <a href={LAB_URL} target="_blank" rel="noopener">{LAB_DOMAIN}&nbsp;↗</a></p>
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
			<h3 class="sort-sub">{c.courtTitle}</h3>
			{#each c.courtBody as p}<p>{p}</p>{/each}
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
				<span class="fx">Tartuffe-Score</span> = 100 × (1 − exp( −{formatConstant(DECAY, currentLang)} ×
				<span class="frac"><span><b>B</b> + {formatConstant(EXTRA_WEIGHT, currentLang)} × ln(1 + <b>E</b>)</span><span><b>A</b> + {SMOOTHING}</span></span>
				))
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

	<!-- Privacy -->
	<section id="privacy" use:reveal>
		<span class="kicker">{c.privacyKicker}</span>
		<h2>{c.privacyTitle}</h2>
		<ol class="limits">
			{#each c.privacy as [head, body]}
				<li><strong>{head}</strong> {body}</li>
			{/each}
		</ol>
		<p class="more"><a href="/about/privacy">{c.privacyLink} →</a></p>
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

	<p class="more event-line"><a href={eventPaths().intro}>{c.pollLink} →</a></p>
</article>

<style>
	.lab-line {
		margin: 0.75rem auto 1rem;
		font-size: 0.9rem;
		color: var(--text-muted);
		text-align: center;
	}
	.page {
		max-width: 46rem;
		margin: 0 auto;
		overflow-x: clip;
	}
	.hero {
		text-align: center;
		margin: 0.5rem 0 3.5rem;
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
		font-family: var(--font-display);
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
		font-family: var(--font-display);
		font-size: clamp(1.6rem, 4.5vw, 2.1rem);
		font-weight: 600;
		line-height: 1.15;
		margin: 0.25rem 0 0.6rem;
		text-wrap: balance;
	}
	h3 {
		font-family: var(--font-display);
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

	/* The second heading of the card: what counts as a fact against a denial. */
	.sort-sub {
		margin-top: 1.5rem;
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
		font-size: 0.78rem;
		letter-spacing: 0.12em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
	.formula {
		font-family: var(--font-display);
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
		font-family: var(--font-display);
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
		font-family: var(--font-display);
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
		text-align: left;
	}
	.neutral-item p {
		margin: 0;
		font-size: 0.9rem;
	}

	.event-line {
		text-align: center;
	}

	/* Scroll reveal: the section's class comes from the action, the root's
	   from the mount, so no-JS and the server-rendered page show everything. */
	.page.reveal-on section:global(.about-reveal) {
		opacity: 0;
		transform: translateY(14px);
		transition:
			opacity 0.7s ease,
			transform 0.7s ease;
	}
	.page.reveal-on section:global(.about-reveal.shown) {
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
</style>
