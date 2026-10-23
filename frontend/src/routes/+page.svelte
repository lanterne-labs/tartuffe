<script lang="ts">
	// The landing page: the contradiction checker first (the slogan, one
	// made-up example of a contradiction and its score, the ranking and the
	// feed), then the lab's temporary event, the private presidential vote,
	// in its own colour with its end date, then the lab itself. The ranking is
	// at /dashboard.
	import { eventPaths } from '$lib/event';
	import { onMount } from 'svelte';
	import { lang, type Lang } from '$lib/i18n';
	import { API_BASE } from '$lib/config';
	import type { PollEvent } from '$lib/types';
	import Mirror from '$lib/components/about/Mirror.svelte';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	// Fades a section in when it scrolls into view. Without JS, or with
	// reduced motion, the section is simply shown.
	function reveal(node: HTMLElement) {
		if (typeof IntersectionObserver === 'undefined') return;
		if (window.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
		node.classList.add('reveal');
		const io = new IntersectionObserver(
			(entries) => {
				if (entries.some((e) => e.isIntersecting)) {
					node.classList.add('shown');
					io.disconnect();
				}
			},
			{ rootMargin: '0px 0px -10% 0px' }
		);
		io.observe(node);
		return { destroy: () => io.disconnect() };
	}

	const POLL = eventPaths();
	let featured = $state<PollEvent | null>(null);
	onMount(async () => {
		try {
			const res = await fetch(`${API_BASE}/api/polls`);
			if (!res.ok) throw new Error(`HTTP ${res.status}`);
			featured = ((await res.json()) as PollEvent[]).find((e) => e.featured) ?? null;
		} catch (err) {
			console.error('failed to load the featured poll', err);
		}
	});
	function dateText(iso: string): string {
		return new Date(iso).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}
	// The score of the example: one contradiction among few facts.
	const SCORE = 92;

	const content = {
		fr: {
			title: 'Ils disent. Ils agissent. On compare.',
			lead: 'La cohérence, pas la vérité : on ne dit pas qui a raison, on montre qui se contredit. Preuves à l’appui.',
			pills: ['100 % sourcé', 'Mêmes règles pour tous', 'Corrigé par vous'],
			ctaMain: 'Voir le classement',
			ctaFeed: 'Lire le fil',

			exKicker: 'En un coup d’œil',
			exTitle: 'Une contradiction, un score',
			exWho: 'Un élu',
			exTag: 'Exemple',
			exSaid: '« Je ne toucherai jamais à l’âge de départ à la retraite. »',
			exSaidSrc: 'Interview',
			exDid: 'Vote pour le report de l’âge de départ à 64 ans.',
			exDidSrc: 'Vote à l’Assemblée',
			exClash: 'Contradiction',
			exScore: 'Score d’honnêteté',
			steps: [
				['On écoute', 'Ce qu’il dit, ce qu’il vote.'],
				['On compare', 'Les deux s’opposent ? C’est noté.'],
				['On montre', 'Le score baisse. Sources à l’appui.']
			],
			exNote: 'Cohérence, pas vérité.',

			svcKicker: 'Deux façons de s’en servir',
			svcTitle: 'Tout est public',
			services: [
				{ href: '/dashboard', icon: 'rank', title: 'Le classement', body: 'Tous les élus et partis, notés sur 100. Cherchez, comparez.', cta: 'Ouvrir' },
				{ href: '/feed', icon: 'feed', title: 'Le fil', body: 'Les dernières contradictions, en direct. Un +1 les fait remonter.', cta: 'Lire' }
			],
			aboutLink: 'Comment on calcule le score',

			evTag: 'Expérience temporaire du labo',
			evTitle: 'Présidentielle 2027 : le vote en ligne, vraiment privé',
			evBody: 'Classez jusqu’à 3 candidats, ou votez blanc, avec votre passeport ou votre carte d’identité. Personne, ni nous ni l’État, ne peut savoir qui a voté quoi, et chacun peut recompter. Le score d’honnêteté de chaque candidat est juste à côté.',
			evEnds: 'Jusqu’au {d}',
			evCta: 'Voter',
			evMore: 'Découvrir l’expérience',

			labKicker: 'Le labo',
			labTitle: 'Un laboratoire pour la démocratie',
			labBody: 'tartuffe.me est un laboratoire de recherche et développement des outils démocratiques : plus de transparence, plus de redevabilité, des preuves plutôt que des opinions. Le détecteur de contradictions est notre premier produit ; le vote privé, notre première expérience.',
			labCta: 'Découvrir le labo'
		},
		en: {
			title: 'They say. They act. We compare.',
			lead: 'Consistency, not truth: we don’t say who is right, we show who contradicts themselves. With the proof.',
			pills: ['100 % sourced', 'Same rules for all', 'Corrected by you'],
			ctaMain: 'See the ranking',
			ctaFeed: 'Read the feed',

			exKicker: 'At a glance',
			exTitle: 'One contradiction, one score',
			exWho: 'A politician',
			exTag: 'Example',
			exSaid: '“I will never touch the retirement age.”',
			exSaidSrc: 'Interview',
			exDid: 'Votes to raise the retirement age to 64.',
			exDidSrc: 'Vote in Parliament',
			exClash: 'Contradiction',
			exScore: 'Honesty score',
			steps: [
				['We listen', 'What they say, how they vote.'],
				['We compare', 'The two clash? It’s on record.'],
				['We show', 'The score drops. With the sources.']
			],
			exNote: 'Consistency, not truth.',

			svcKicker: 'Two ways to use it',
			svcTitle: 'Everything is public',
			services: [
				{ href: '/dashboard', icon: 'rank', title: 'The ranking', body: 'Every politician and party, scored out of 100. Search, compare.', cta: 'Open' },
				{ href: '/feed', icon: 'feed', title: 'The feed', body: 'The latest contradictions, live. A +1 pushes them up.', cta: 'Read' }
			],
			aboutLink: 'How the score is computed',

			evTag: 'A temporary experiment of the lab',
			evTitle: 'Presidential 2027: online voting, truly private',
			evBody: 'Rank up to 3 candidates, or vote blank, with your passport or ID card. Nobody, neither we nor the State, can know who voted what, and anyone can recount. Each candidate’s honesty score is right beside.',
			evEnds: 'Until {d}',
			evCta: 'Vote',
			evMore: 'Discover the experiment',

			labKicker: 'The lab',
			labTitle: 'A lab for democracy',
			labBody: 'tartuffe.me is a research and development lab for democratic tools: more transparency, more accountability, evidence rather than opinions. The contradiction checker is our first product; the private vote, our first experiment.',
			labCta: 'Discover the lab'
		}
	};
	let c = $derived(content[currentLang]);

	// The ring of the score: its circumference and the part left empty.
	const R = 42;
	const C = 2 * Math.PI * R;
</script>

<svelte:head>
	<title>tartuffe.me — {c.title}</title>
</svelte:head>

{#snippet icon(name: string)}
	{#if name === 'rank'}
		<path d="M5 20V11M12 20V5M19 20v-6" />
	{:else if name === 'feed'}
		<path d="M5 6h14M5 12h14M5 18h9" />
	{:else}
		<path d="M5 12l4 4 10-10" /><rect x="3" y="3" width="18" height="18" rx="4" />
	{/if}
{/snippet}

<article class="landing">
	<header class="hero">
		<Mirror />
		<h1>{c.title}</h1>
		<p class="lead">{c.lead}</p>
		<ul class="pills">
			{#each c.pills as p}<li>{p}</li>{/each}
		</ul>
		<div class="ctas">
			<a class="cta" href="/dashboard">{c.ctaMain}</a>
			<a class="cta cta-ghost" href="/feed">{c.ctaFeed} →</a>
		</div>
	</header>

	<section class="example" use:reveal>
		<span class="kicker">{c.exKicker}</span>
		<h2>{c.exTitle}</h2>

		<div class="ex-card">
			<div class="ex-who">
				<svg class="ex-avatar" viewBox="0 0 48 48" aria-hidden="true">
					<circle cx="24" cy="24" r="24" class="av-bg" />
					<circle cx="24" cy="19" r="8" class="av-fg" />
					<path d="M9 42c2-9 8-13 15-13s13 4 15 13" class="av-fg" />
				</svg>
				<span class="ex-name">{c.exWho}</span>
				<span class="ex-tag">{c.exTag}</span>
			</div>

			<div class="ex-body">
				<div class="ex-facts">
					<div class="ex-fact">
						<span class="ex-year">2019</span>
						<p>{c.exSaid}</p>
						<span class="ex-src">{c.exSaidSrc}</span>
					</div>
					<div class="ex-clash" aria-hidden="true"><span>⚡ {c.exClash}</span></div>
					<div class="ex-fact">
						<span class="ex-year">2023</span>
						<p>{c.exDid}</p>
						<span class="ex-src">{c.exDidSrc}</span>
					</div>
				</div>

				<div class="ex-score">
					<svg viewBox="0 0 100 100" aria-hidden="true">
						<circle cx="50" cy="50" r={R} class="ring-bg" />
						<circle cx="50" cy="50" r={R} class="ring" stroke-dasharray={C} style="--off: {C * (1 - SCORE / 100)}; --full: {C}" />
					</svg>
					<span class="ex-num">{SCORE}<small>/100</small></span>
					<span class="ex-label">{c.exScore}</span>
				</div>
			</div>
		</div>

		<ol class="steps">
			{#each c.steps as [head, body], i}
				<li><span class="step-n">{i + 1}</span><strong>{head}</strong><span>{body}</span></li>
			{/each}
		</ol>
	</section>

	<section class="services" use:reveal>
		<span class="kicker">{c.svcKicker}</span>
		<h2>{c.svcTitle}</h2>
		<div class="svc-grid">
			{#each c.services as s (s.href)}
				<a class="svc" href={s.href}>
					<svg class="svc-icon" viewBox="0 0 24 24" aria-hidden="true">{@render icon(s.icon)}</svg>
					<h3>{s.title}</h3>
					<p>{s.body}</p>
					<span class="svc-cta">{s.cta} →</span>
				</a>
			{/each}
		</div>
		<p class="more"><a href="/about">{c.aboutLink} →</a></p>
	</section>

	<section class="event" use:reveal>
		<div class="ev-head">
			<span class="ev-tag">{c.evTag}</span>
			{#if featured?.closes_at}
				<span class="ev-ends">{c.evEnds.replace('{d}', dateText(featured.closes_at))}</span>
			{/if}
		</div>
		<h2>{c.evTitle}</h2>
		<p>{c.evBody}</p>
		<div class="ev-actions">
			<a class="ev-cta" href={featured ? eventPaths(featured.slug).vote : POLL.vote}>{c.evCta} →</a>
			<a class="ev-more" href={featured ? eventPaths(featured.slug).intro : POLL.intro}>{c.evMore}</a>
		</div>
	</section>

	<section class="lab-teaser" use:reveal>
		<span class="kicker">{c.labKicker}</span>
		<h2>{c.labTitle}</h2>
		<p>{c.labBody}</p>
		<p class="more"><a href="/lab">{c.labCta} →</a></p>
	</section>
</article>

<style>
	.event {
		margin: 4.5rem 0 0;
		padding: 1.6rem 1.75rem;
		border: 1px dashed var(--event);
		border-radius: var(--radius-lg);
		background: color-mix(in oklch, var(--event) 7%, var(--bg-card));
	}
	.ev-head {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: 0.5rem 1rem;
		align-items: center;
	}
	.ev-tag {
		font-size: 0.7rem;
		font-weight: 700;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		color: var(--event);
	}
	.ev-ends {
		font-size: 0.8rem;
		font-weight: 600;
		padding: 0.2rem 0.65rem;
		border-radius: var(--radius-full);
		border: 1px solid var(--event);
		color: var(--text-primary);
	}
	.event h2 {
		margin: 0.6rem 0 0.6rem;
		text-align: left;
	}
	.event p {
		margin: 0 0 1.1rem;
		color: var(--text-secondary);
		line-height: 1.6;
		max-width: 40rem;
	}
	.ev-actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.75rem 1.25rem;
	}
	.ev-cta {
		padding: 0.6rem 1.25rem;
		border-radius: var(--radius-full);
		background: var(--event-strong);
		color: white;
		font-weight: 600;
		text-decoration: none;
	}
	.ev-cta:hover {
		background: var(--event);
	}
	.ev-more {
		color: var(--text-secondary);
		font-size: 0.9rem;
	}
	.lab-teaser {
		margin: 4.5rem 0 1rem;
		text-align: center;
	}
	.lab-teaser p {
		max-width: 38rem;
		margin: 0 auto 0.75rem;
		color: var(--text-secondary);
		line-height: 1.6;
		text-wrap: balance;
	}
	.landing {
		max-width: 52rem;
		margin: 0 auto;
		overflow-x: clip;
	}
	.hero {
		text-align: center;
		margin: 1rem 0 5rem;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(2.2rem, 7vw, 3.6rem);
		font-weight: 600;
		line-height: 1.05;
		margin: 0.5rem auto 1.1rem;
		max-width: 40rem;
		text-wrap: balance;
	}
	.lead {
		font-size: 1.12rem;
		color: var(--text-secondary);
		line-height: 1.6;
		max-width: 36rem;
		margin: 0 auto 1.4rem;
		text-wrap: balance;
	}
	.pills {
		list-style: none;
		padding: 0;
		margin: 0 0 2rem;
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
	.ctas {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		gap: 0.75rem;
	}
	.cta {
		display: inline-flex;
		align-items: center;
		padding: 0.75rem 1.4rem;
		border-radius: var(--radius-full);
		background: var(--accent);
		color: #fff;
		font-weight: 600;
		text-decoration: none;
		transition: background 0.15s;
	}
	.cta:hover {
		background: var(--accent-hover);
		color: #fff;
	}
	.cta-ghost {
		background: transparent;
		border: 1px solid var(--border-card-hover);
		color: var(--text-primary);
	}
	.cta-ghost:hover {
		background: var(--bg-card);
		color: var(--text-primary);
	}

	section {
		margin-bottom: 5rem;
		text-align: center;
	}
	.kicker {
		display: block;
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.14em;
		text-transform: uppercase;
		color: var(--accent-hover);
	}
	h2 {
		font-family: var(--font-serif);
		font-size: clamp(1.7rem, 4.5vw, 2.3rem);
		font-weight: 600;
		line-height: 1.15;
		margin: 0.3rem 0 2rem;
		text-wrap: balance;
	}

	/* The example */
	.ex-card {
		max-width: 40rem;
		margin: 0 auto;
		padding: 1.5rem;
		text-align: left;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		box-shadow: var(--shadow-card);
	}
	.ex-who {
		display: flex;
		align-items: center;
		gap: 0.7rem;
		margin-bottom: 1.25rem;
	}
	.ex-avatar {
		width: 44px;
		height: 44px;
	}
	.av-bg {
		fill: var(--bg-secondary);
	}
	.av-fg {
		fill: var(--text-muted);
	}
	.ex-name {
		font-weight: 600;
	}
	.ex-tag {
		font-size: 0.7rem;
		padding: 0.1rem 0.55rem;
		border-radius: var(--radius-full);
		border: 1px dashed var(--border-card-hover);
		color: var(--text-muted);
	}
	.ex-body {
		display: grid;
		grid-template-columns: minmax(0, 1fr) 9rem;
		gap: 1.5rem;
		align-items: center;
	}
	.ex-facts {
		display: flex;
		flex-direction: column;
	}
	.ex-fact {
		padding: 0.9rem 1rem;
		border-radius: var(--radius-md);
		background: var(--bg-secondary);
	}
	.ex-fact p {
		margin: 0.25rem 0 0.4rem;
		font-size: 1rem;
		line-height: 1.5;
	}
	.ex-year {
		font-size: 0.72rem;
		font-weight: 700;
		color: var(--text-muted);
	}
	.ex-src {
		font-size: 0.72rem;
		color: var(--text-muted);
	}
	.ex-clash {
		position: relative;
		display: flex;
		justify-content: center;
		padding: 0.7rem 0;
	}
	.ex-clash::before {
		content: '';
		position: absolute;
		top: 0;
		bottom: 0;
		left: 50%;
		border-left: 2px dashed var(--red);
		opacity: 0.6;
	}
	.ex-clash span {
		position: relative;
		padding: 0.2rem 0.7rem;
		border-radius: var(--radius-full);
		background: var(--error-bg);
		border: 1px solid var(--error-border);
		color: var(--red);
		font-size: 0.75rem;
		font-weight: 600;
		backdrop-filter: blur(6px);
	}
	.ex-score {
		position: relative;
		display: flex;
		flex-direction: column;
		align-items: center;
		text-align: center;
	}
	.ex-score svg {
		width: 8.5rem;
		height: 8.5rem;
		transform: rotate(-90deg);
	}
	.ring-bg,
	.ring {
		fill: none;
		stroke-width: 7;
	}
	.ring-bg {
		stroke: var(--bg-secondary);
	}
	.ring {
		stroke: var(--green);
		stroke-linecap: round;
		stroke-dashoffset: var(--off);
	}
	.ex-num {
		position: absolute;
		top: 4.25rem;
		transform: translateY(-50%);
		font-family: var(--font-serif);
		font-size: 2.3rem;
		font-weight: 600;
		font-variant-numeric: lining-nums;
		line-height: 1;
	}
	.ex-num small {
		font-size: 1rem;
		color: var(--text-muted);
	}
	.ex-label {
		margin-top: 0.4rem;
		font-size: 0.8rem;
		color: var(--text-secondary);
	}
	.steps {
		list-style: none;
		padding: 0;
		margin: 2.5rem auto 0;
		max-width: 44rem;
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 1rem;
	}
	.steps li {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.3rem;
	}
	.step-n {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 2rem;
		height: 2rem;
		margin-bottom: 0.3rem;
		border-radius: 50%;
		border: 1px solid var(--accent);
		color: var(--accent-hover);
		font-weight: 600;
		font-size: 0.9rem;
	}
	.steps strong {
		font-family: var(--font-serif);
		font-size: 1.25rem;
		font-weight: 600;
	}
	.steps span:last-child {
		color: var(--text-secondary);
		font-size: 0.92rem;
	}

	/* The services */
	.svc-grid {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 1rem;
		text-align: left;
	}
	.svc {
		display: flex;
		flex-direction: column;
		padding: 1.5rem 1.4rem 1.25rem;
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		border-radius: var(--radius-lg);
		color: var(--text-primary);
		text-decoration: none;
		transition:
			border-color 0.2s,
			transform 0.2s;
	}
	.svc:hover {
		border-color: var(--accent);
		transform: translateY(-2px);
	}
	.svc-icon {
		width: 28px;
		height: 28px;
		fill: none;
		stroke: var(--accent-hover);
		stroke-width: 1.8;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.svc h3 {
		font-family: var(--font-serif);
		font-size: 1.35rem;
		font-weight: 600;
		margin: 0.9rem 0 0.4rem;
	}
	.svc p {
		flex: 1;
		margin: 0 0 1rem;
		color: var(--text-secondary);
		line-height: 1.55;
		font-size: 0.95rem;
	}
	.svc-cta {
		font-weight: 600;
		font-size: 0.9rem;
		color: var(--accent-hover);
	}
	.more {
		margin-top: 2rem;
	}
	.more a {
		color: var(--text-secondary);
	}
	.more a:hover {
		color: var(--text-primary);
	}

	/* Scroll reveal (added by the action, so no-JS shows everything). The
	   ring fills when the example comes into view. */
	section:global(.reveal) {
		opacity: 0;
		transform: translateY(14px);
		transition:
			opacity 0.7s ease,
			transform 0.7s ease;
	}
	section:global(.reveal.shown) {
		opacity: 1;
		transform: none;
	}
	section:global(.reveal) .ring {
		stroke-dashoffset: var(--full);
		transition: stroke-dashoffset 1.4s ease 0.3s;
	}
	section:global(.reveal.shown) .ring {
		stroke-dashoffset: var(--off);
	}

	@media (max-width: 680px) {
		.hero {
			margin-bottom: 3.5rem;
		}
		section {
			margin-bottom: 3.5rem;
		}
		.ex-card {
			padding: 1.1rem;
		}
		.ex-body,
		.svc-grid {
			grid-template-columns: minmax(0, 1fr);
		}
		.steps {
			grid-template-columns: minmax(0, 1fr);
			gap: 1.5rem;
		}
	}
</style>
