<script lang="ts">
	import TartuffeScore, { tartuffeOf } from '$lib/components/TartuffeScore.svelte';
	// The landing page: the contradiction checker first (the question, the
	// podium of the day, the contradiction of the day from the feed and its
	// score, or a made-up example while it loads), then the lab's temporary event, the private presidential vote,
	// in its own colour with its end date, then the lab itself. The ranking is
	// at /dashboard.
	import { eventPaths } from '$lib/event';
	import { onMount } from 'svelte';
	import { lang, whyOf, type Lang } from '$lib/i18n';
	import { API_BASE } from '$lib/config';
	import type { PollEvent, FeedItem, FeedPage } from '$lib/types';
	import Podium from '$lib/components/Podium.svelte';
	import { sourceDomain } from '$lib/sources';
	import { factYear } from '$lib/dates';
	import { displayName } from '$lib/share';
	import BrandMark from '$lib/components/BrandMark.svelte';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	// Fades a section in when it scrolls into view. Without JS, with reduced
	// motion or on a touch screen (a fast flick outruns the fade and leaves
	// screens blank), the section is simply shown. The action runs after
	// mount, so the server-rendered page is never hidden before it; a section
	// already on screen at mount is left alone, or it would vanish and fade
	// back in.
	function reveal(node: HTMLElement) {
		if (typeof IntersectionObserver === 'undefined') return;
		if (window.matchMedia('(prefers-reduced-motion: reduce), (hover: none)').matches) return;
		if (node.getBoundingClientRect().top < window.innerHeight) return;
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
	// The contradiction of the day: the hottest of the feed. Without one
	// (an empty feed, an error) the made-up example stands in.
	let clash = $state<FeedItem | null>(null);
	onMount(async () => {
		const [polls, feed] = await Promise.allSettled([fetch(`${API_BASE}/api/polls`), fetch(`${API_BASE}/api/feed?sort=hot&kind=contradiction&limit=1`)]);
		try {
			if (polls.status === 'rejected' || !polls.value.ok) throw new Error(polls.status === 'rejected' ? String(polls.reason) : `HTTP ${polls.value.status}`);
			featured = ((await polls.value.json()) as PollEvent[]).find((e) => e.featured) ?? null;
		} catch (err) {
			console.error('failed to load the featured poll', err);
		}
		try {
			if (feed.status === 'rejected' || !feed.value.ok) throw new Error(feed.status === 'rejected' ? String(feed.reason) : `HTTP ${feed.value.status}`);
			const first = ((await feed.value.json()) as FeedPage).items?.[0];
			if (first?.other) clash = first;
		} catch (err) {
			console.error('failed to load the contradiction of the day', err);
		}
	});
	// The two sides of the clash, the older first, as the example reads.
	let sides = $derived.by(() => {
		if (!clash?.other) return null;
		const [x, y] = [clash.fact, clash.other];
		const [a, b] = (x.date ?? '') <= (y.date ?? '') ? [x, y] : [y, x];
		const side = (f: typeof x) => ({
			year: factYear(f.date, f.date_precision),
			text: currentLang === 'en' ? f.text_en || f.text : f.quote || f.text,
			src: f.source_urls?.[0] ? sourceDomain(f.source_urls[0]) : ''
		});
		return { a: side(a), b: side(b), why: whyOf(clash, currentLang), rise: Math.max(1, Math.round(-(clash.score_effect ?? 0))) };
	});
	function dateText(iso: string): string {
		return new Date(iso).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}
	// The example: one contradiction among few facts, as the API would
	// give it (an honesty score, 0 to 1), before and after it. The rise
	// shows as the feed shows a score's move: a red ▲ and the change.
	const HONESTY_BEFORE = 0.78;
	const HONESTY = 0.62;
	const RISE = (tartuffeOf(HONESTY) ?? 0) - (tartuffeOf(HONESTY_BEFORE) ?? 0);

	// The site's source, public under the AGPL: the third way in, beside
	// the ranking and the feed.
	const CODE = 'https://github.com/lanterne-labs/mirror';

	const content = {
		fr: {
			title: 'Cohérent ou tartuffe ?',
			lead: 'Un score par élu : ses contradictions entre ce qu’il dit, ce qu’il vote et ce que la justice a tranché. Plus il est haut, plus il est tartuffe.',
			pills: ['Tout est sourcé', 'Les mêmes règles pour tous', 'Vous pouvez nous corriger'],
			ctaMain: 'Voir le classement',
			ctaFeed: 'Lire le fil',

			scoreName: 'Le Tartuffe-Score mesure les contradictions',
			scoreWhat: 'Celles d’un élu avec lui-même : entre ce qu’il dit, ce qu’il promet et ce qu’il vote. Et avec la justice : nier ce qu’un tribunal a établi à son sujet compte aussi. Plus il se contredit, plus son score monte.',
			scoreLow: 'Cohérent',
			scoreHigh: 'Tartuffe',
			exRise: 'après cette contradiction',
			exRiseLabel: 'Le Tartuffe-Score monte de {n} points',
			exTitle: 'Une contradiction, et le Tartuffe-Score monte',
			exWho: 'Un député',
			exTag: 'Exemple',
			exTagLive: 'Aujourd’hui',
			exLink: 'La fiche et les sources',
			exSaid: '« Je ne toucherai jamais à l’âge de départ à la retraite. »',
			exSaidSrc: 'En interview',
			exDid: 'Il vote le report de l’âge de départ à 64 ans.',
			exDidSrc: 'Scrutin à l’Assemblée',
			exWhy: 'En 2019, il s’engageait à ne jamais toucher à l’âge de départ à la retraite. En 2023, il vote son report à 64 ans : son vote va contre son engagement.',
			exClash: 'Contradiction',
			steps: [
				['On relève', 'Ses déclarations, ses votes, et les décisions de justice qui le concernent.'],
				['On confronte', 'Quand ils se contredisent, on le note.'],
				['On publie', 'Son Tartuffe-Score monte, preuves à l’appui.']
			],
			exNote: 'On mesure la cohérence, pas la vérité.',

			svcKicker: 'Suivre et vérifier',
			svcTitle: 'Tout est en accès libre',
			services: [
				{ href: '/dashboard', icon: 'rank', title: 'Le classement', body: 'Chaque élu et chaque parti, de cohérent à tartuffe. Cherchez un nom, comparez.', cta: 'Ouvrir' },
				{ href: '/feed', icon: 'feed', title: 'Le fil', body: 'Les dernières contradictions, au fil de l’eau. Un +1 les fait remonter.', cta: 'Lire' },
				{ href: CODE, icon: 'code', title: 'Le code', body: 'Le site est open source : la collecte, la détection, le calcul du score. Lisez-le, vérifiez-le, corrigez-le.', cta: 'Voir le code' }
			],
			aboutLink: 'Comment le score est calculé',
			podiumKicker: 'Aujourd’hui',
			podiumLink: 'Tout le classement',

			evTag: 'Expérience temporaire du labo',
			evTitle: 'Présidentielle 2027 : voter en ligne, vraiment en secret',
			evBody: 'Classez jusqu’à trois candidats ou votez blanc, avec votre passeport. Personne, ni nous ni l’État, ne peut savoir qui a voté quoi, et chacun peut recompter. Le Tartuffe-Score de chaque candidat s’affiche à côté.',
			evEnds: 'Jusqu’au {d}',
			evCta: 'Voter',
			evMore: 'Comment ça marche',

			labKicker: 'Le labo',
			labTitle: 'Un laboratoire au service de la démocratie',
			labBody: 'tartuffe.me conçoit et teste des outils pour une démocratie plus transparente. Les nouvelles technologies permettent enfin à chacun de suivre ce que disent et font les élus, sources à l’appui, de façon lisible. Le détecteur de contradictions est notre premier produit, le vote secret en ligne notre première expérience.',
			labCta: 'Découvrir le labo'
		},
		en: {
			title: 'Coherent or tartuffe?',
			lead: 'One score per politician: their contradictions between what they say, how they vote and what the courts ruled. The higher it is, the more tartuffe.',
			pills: ['100 % sourced', 'Same rules for all', 'Corrected by you'],
			ctaMain: 'See the ranking',
			ctaFeed: 'Read the feed',

			scoreName: 'The Tartuffe-Score measures contradictions',
			scoreWhat: 'A politician’s contradictions with themselves: between what they say, what they promise and how they vote. And with the courts: denying what a court has established about them counts too. The more they contradict themselves, the higher their score.',
			scoreLow: 'Coherent',
			scoreHigh: 'Tartuffe',
			exRise: 'after this contradiction',
			exRiseLabel: 'The Tartuffe-Score rises by {n} points',
			exTitle: 'One contradiction, the Tartuffe-Score rises',
			exWho: 'A politician',
			exTag: 'Example',
			exTagLive: 'Today',
			exLink: 'The record and the sources',
			exSaid: '“I will never touch the retirement age.”',
			exSaidSrc: 'Interview',
			exDid: 'Votes to raise the retirement age to 64.',
			exDidSrc: 'Vote in Parliament',
			exWhy: 'In 2019 he pledged never to touch the retirement age. In 2023 he votes to raise it to 64: his vote goes against his pledge.',
			exClash: 'Contradiction',
			steps: [
				['We listen', 'What they say, how they vote, and what courts ruled about them.'],
				['We compare', 'The two clash? It’s on record.'],
				['We show', 'The Tartuffe-Score rises. With the sources.']
			],
			exNote: 'Consistency, not truth.',

			svcKicker: 'Follow and verify',
			svcTitle: 'Everything is public',
			services: [
				{ href: '/dashboard', icon: 'rank', title: 'The ranking', body: 'Every politician and party, from coherent to tartuffe. Search, compare.', cta: 'Open' },
				{ href: '/feed', icon: 'feed', title: 'The feed', body: 'The latest contradictions, live. A +1 pushes them up.', cta: 'Read' },
				{ href: CODE, icon: 'code', title: 'The code', body: 'The site is open source: the collection, the detection, the score. Read it, check it, fix it.', cta: 'See the code' }
			],
			aboutLink: 'How the score is computed',
			podiumKicker: 'Today',
			podiumLink: 'The whole ranking',

			evTag: 'A temporary experiment of the lab',
			evTitle: 'Presidential 2027: online voting, truly private',
			evBody: 'Rank up to 3 candidates, or vote blank, with your passport. Nobody, neither we nor the State, can know who voted what, and anyone can recount. Each candidate’s Tartuffe-Score is right beside.',
			evEnds: 'Until {d}',
			evCta: 'Vote',
			evMore: 'How it works',

			labKicker: 'The lab',
			labTitle: 'A lab for democracy',
			labBody: 'tartuffe.me designs and tests tools for a more transparent democracy. New technology finally lets anyone follow what elected officials say and do, with sources, in a readable way. The contradiction checker is our first product; the private vote, our first experiment.',
			labCta: 'Discover the lab'
		}
	};
	let c = $derived(content[currentLang]);

</script>

<svelte:head>
	<title>tartuffe.me — {c.title}</title>
</svelte:head>

{#snippet icon(name: string)}
	{#if name === 'rank'}
		<path d="M5 20V11M12 20V5M19 20v-6" />
	{:else if name === 'feed'}
		<path d="M5 6h14M5 12h14M5 18h9" />
	{:else if name === 'code'}
		<path d="M8 7l-5 5 5 5M16 7l5 5-5 5M13.5 5l-3 14" />
	{:else}
		<path d="M5 12l4 4 10-10" /><rect x="3" y="3" width="18" height="18" rx="4" />
	{/if}
{/snippet}

<article class="landing">
	<header class="hero">
		<BrandMark size="hero" />
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

	<section class="podium" use:reveal>
		<span class="kicker">{c.podiumKicker}</span>
		<Podium lang={currentLang} />
		<p class="more"><a href="/dashboard">{c.podiumLink} →</a></p>
	</section>

	<section class="example" use:reveal>
		<h2>{c.scoreName}</h2>
		<div class="ex-card score-intro">
			<p class="si-what">{c.scoreWhat}</p>
			<div class="si-scale" aria-hidden="true">
				<span class="si-bar"></span>
				<span class="si-ends"><span>0 · {c.scoreLow}</span><span>{c.scoreHigh} · 100</span></span>
			</div>
		</div>

		<h2>{c.exTitle}</h2>

		<div class="ex-card">
			{#if clash && sides}
				<div class="ex-who">
					{#if clash.person.image_url}
						<img class="ex-avatar" src={clash.person.image_url} alt="" />
					{:else}
						<svg class="ex-avatar" viewBox="0 0 48 48" aria-hidden="true"><circle cx="24" cy="24" r="24" class="av-bg" /><circle cx="24" cy="19" r="8" class="av-fg" /><path d="M9 42c2-9 8-13 15-13s13 4 15 13" class="av-fg" /></svg>
					{/if}
					<a class="ex-name" href="/person/{clash.person.id}?c={clash.id}">{displayName(clash.person.name)}</a>
					<span class="ex-tag ex-tag--live">{c.exTagLive}</span>
				</div>
				<div class="ex-body">
					<div class="ex-facts">
						<div class="ex-fact">
							<span class="ex-year">{sides.a.year}</span>
							<p>{sides.a.text}</p>
							<span class="ex-src">{sides.a.src}</span>
						</div>
						<div class="ex-clash" aria-hidden="true"><span>⚡ {c.exClash}</span></div>
						<div class="ex-fact">
							<span class="ex-year">{sides.b.year}</span>
							<p>{sides.b.text}</p>
							<span class="ex-src">{sides.b.src}</span>
						</div>
						{#if sides.why}
							<p class="ex-why">{sides.why}</p>
						{/if}
					</div>
					<div class="ex-score">
						<TartuffeScore honesty={clash.person.honesty_score} lang={currentLang} size="lg" named={false} animate />
						<span class="ex-rise" aria-label={c.exRiseLabel.replace('{n}', String(sides.rise))}>
							<span class="ex-ticker" aria-hidden="true"><span>▲</span> +{sides.rise}</span>
							<span class="ex-rise-note" aria-hidden="true">{c.exRise}</span>
						</span>
					</div>
				</div>
				<a class="ex-link" href="/person/{clash.person.id}?c={clash.id}">{c.exLink} →</a>
			{:else}
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
						<p class="ex-why">{c.exWhy}</p>
					</div>
					<div class="ex-score">
						<TartuffeScore honesty={HONESTY} lang={currentLang} size="lg" named={false} animate />
						<span class="ex-rise" aria-label={c.exRiseLabel.replace('{n}', String(RISE))}>
							<span class="ex-ticker" aria-hidden="true"><span>▲</span> +{RISE}</span>
							<span class="ex-rise-note" aria-hidden="true">{c.exRise}</span>
						</span>
					</div>
				</div>
			{/if}
		</div>

		<ol class="steps">
			{#each c.steps as [head, body], i}
				<li><span class="step-n">{i + 1}</span><strong>{head}</strong><span>{body}</span></li>
			{/each}
		</ol>
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
			<!-- The vote's presentation first, never the ballot: a voter learns
			     what the vote is and why it asks for a passport before they vote. -->
			<a class="ev-cta" href={featured ? eventPaths(featured.slug).intro : POLL.intro}>{c.evCta} →</a>
			<a class="ev-more" href={featured ? eventPaths(featured.slug).how : POLL.how}>{c.evMore}</a>
		</div>
	</section>

	<section class="lab-teaser" use:reveal>
		<span class="kicker">{c.labKicker}</span>
		<h2>{c.labTitle}</h2>
		<p>{c.labBody}</p>
		<p class="more"><a href="/lab">{c.labCta} →</a></p>
	</section>
	<section class="services" use:reveal>
		<span class="kicker">{c.svcKicker}</span>
		<h2>{c.svcTitle}</h2>
		<div class="svc-grid">
			{#each c.services as s (s.href)}
				<a class="svc" href={s.href} target={s.href.startsWith('http') ? '_blank' : undefined} rel={s.href.startsWith('http') ? 'noopener noreferrer' : undefined}>
					<svg class="svc-icon" viewBox="0 0 24 24" aria-hidden="true">{@render icon(s.icon)}</svg>
					<h3>{s.title}</h3>
					<p>{s.body}</p>
					<span class="svc-cta">{s.cta} →</span>
				</a>
			{/each}
		</div>
		<p class="more"><a href="/about">{c.aboutLink} →</a></p>
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
		font-size: 0.75rem;
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
	/* A finger needs more than a mouse. */
	@media (pointer: coarse) {
		.ev-more {
			display: inline-flex;
			align-items: center;
			min-height: 2.5rem;
		}
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
		color: var(--on-accent);
		font-weight: 600;
		text-decoration: none;
		transition: background 0.15s;
	}
	.cta:hover {
		background: var(--accent-hover);
		color: var(--on-accent);
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

	/* What the Tartuffe-Score is, before the example: the example's card. */
	.ex-card.score-intro {
		margin-bottom: 3.5rem;
	}
	.si-what {
		margin: 0 0 1.1rem;
		font-size: 1rem;
		line-height: 1.5;
	}
	.si-bar {
		display: block;
		height: 0.5rem;
		border-radius: var(--radius-full);
		background: linear-gradient(90deg, var(--green), var(--amber), var(--red));
	}
	.si-ends {
		display: flex;
		justify-content: space-between;
		margin-top: 0.35rem;
		font-size: 0.75rem;
		font-weight: 700;
		color: var(--text-muted);
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
		border-radius: 50%;
		object-fit: cover;
	}
	.av-bg {
		fill: var(--bg-secondary);
	}
	.av-fg {
		fill: var(--text-muted);
	}
	.ex-name {
		font-weight: 600;
		color: var(--text-primary);
		text-decoration: none;
	}
	a.ex-name:hover {
		text-decoration: underline;
	}
	.ex-tag--live {
		border-style: solid;
		border-color: var(--accent);
		color: var(--accent-hover);
	}
	.ex-link {
		display: inline-block;
		margin-top: 1rem;
		font-size: 0.9rem;
		font-weight: 600;
		color: var(--accent-hover);
		text-decoration: none;
	}
	.ex-link:hover {
		text-decoration: underline;
	}
	.ex-tag {
		font-size: 0.75rem;
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
		font-size: 0.75rem;
		font-weight: 700;
		color: var(--text-muted);
	}
	.ex-src {
		font-size: 0.75rem;
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
	/* Why the two conflict: the context the two lines lack. */
	.ex-why {
		margin: 0.8rem 0 0;
		padding: 0.6rem 0.85rem;
		border-left: 2px solid var(--error-border);
		background: var(--error-bg);
		border-radius: 0 var(--radius-md) var(--radius-md) 0;
		font-size: 0.9rem;
		line-height: 1.55;
		color: var(--text-secondary);
	}
	.ex-score {
		position: relative;
		display: flex;
		flex-direction: column;
		align-items: center;
		text-align: center;
	}
	.ex-score {
		width: min(20rem, 100%);
		margin: 0 auto;
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

	/* The podium of the day, as on the dashboard */
	.podium {
		max-width: 44rem;
		margin-left: auto;
		margin-right: auto;
	}
	.podium .kicker {
		display: block;
		margin-bottom: 1rem;
	}

	/* The services */
	.svc-grid {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
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
	/* The rise, drawn as the feed's ticker: a red ▲ and the change. */
	.ex-rise {
		display: flex;
		flex-direction: column;
		align-items: center;
		margin-top: 0.4rem;
	}
	.ex-ticker {
		display: inline-flex;
		align-items: center;
		gap: 0.25rem;
		padding: 0.15rem 0.6rem;
		border-radius: var(--radius-full);
		background: var(--error-bg);
		border: 1px solid var(--error-border);
		color: var(--red);
		font-weight: 700;
		font-variant-numeric: tabular-nums;
	}
	.ex-ticker span {
		font-size: 0.7rem;
	}
	.ex-rise-note {
		margin-top: 0.25rem;
		font-size: 0.75rem;
		color: var(--text-muted);
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
	/* The example's marker slides once the section is in view. */
	section:global(.reveal) :global(.marker--in) {
		animation-play-state: paused;
	}
	section:global(.reveal.shown) :global(.marker--in) {
		animation-play-state: running;
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
