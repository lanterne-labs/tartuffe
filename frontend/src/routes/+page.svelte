<script lang="ts">
	// The landing page. The hero explains on the left and shows a contradiction
	// on the right at once (the hottest of the feed); then the three ways in
	// (the ranking, the feed, the code), the latest contradictions (a few: the
	// feed has them all), the podium, the score in a few lines, and the lab
	// with its temporary event, the private presidential vote, in its own
	// colour. The ranking is at /dashboard.
	import { eventPaths } from '$lib/event';
	import { onMount } from 'svelte';
	import { lang, type Lang } from '$lib/i18n';
	import { API_BASE } from '$lib/config';
	import type { PollEvent, FeedItem, FeedPage } from '$lib/types';
	import Podium from '$lib/components/Podium.svelte';
	import ClashCard from '$lib/components/ClashCard.svelte';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	const POLL = eventPaths();
	let featured = $state<PollEvent | null>(null);
	// The contradiction of the hero: the hottest of the feed.
	let clash = $state<FeedItem | null>(null);
	// The latest contradictions: a few under the hero, and how many are new
	// today (else this week), said beside the date.
	const LATEST = 3;
	let latest = $state<FeedItem[]>([]);
	let fresh = $state<{ n: number; week: boolean } | null>(null);
	let loaded = $state(false);

	onMount(async () => {
		const [polls, hot, recent] = await Promise.allSettled([
			fetch(`${API_BASE}/api/polls`),
			fetch(`${API_BASE}/api/feed?sort=hot&kind=contradiction&limit=1`),
			fetch(`${API_BASE}/api/feed?sort=new&kind=contradiction&limit=50`)
		]);
		async function json<T>(r: PromiseSettledResult<Response>): Promise<T> {
			if (r.status === 'rejected' || !r.value.ok) throw new Error(r.status === 'rejected' ? String(r.reason) : `HTTP ${r.value.status}`);
			return (await r.value.json()) as T;
		}
		try {
			featured = (await json<PollEvent[]>(polls)).find((e) => e.featured) ?? null;
		} catch (err) {
			console.error('failed to load the featured poll', err);
		}
		try {
			const first = (await json<FeedPage>(hot)).items?.[0];
			if (first?.other) clash = first;
		} catch (err) {
			console.error('failed to load the contradiction of the day', err);
		}
		try {
			const items = (await json<FeedPage>(recent)).items ?? [];
			const day = new Date().toDateString();
			const today = items.filter((it) => new Date(it.at).toDateString() === day).length;
			const week = items.filter((it) => Date.now() - new Date(it.at).getTime() < 7 * 86_400_000).length;
			fresh = today > 0 ? { n: today, week: false } : week > 0 ? { n: week, week: true } : null;
			latest = items.filter((it) => it.other && it.id !== clash?.id).slice(0, LATEST);
		} catch (err) {
			console.error('failed to load the latest contradictions', err);
		}
		loaded = true;
	});

	function dateText(iso: string): string {
		return new Date(iso).toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { day: 'numeric', month: 'long', year: 'numeric' });
	}
	let today = $derived.by(() => {
		const t = new Date().toLocaleDateString(currentLang === 'en' ? 'en-GB' : 'fr-FR', { weekday: 'long', day: 'numeric', month: 'long' });
		return t.charAt(0).toUpperCase() + t.slice(1);
	});

	// The site's source, public under the AGPL: the third way in, beside
	// the ranking and the feed.
	const CODE = 'https://github.com/lanterne-labs/mirror';

	const content = {
		fr: {
			title: 'Cohérent ou tartuffe ?',
			lead: 'Ce que les élus disent, ce qu’ils votent, ce que la justice a tranché. Quand ça ne colle pas, on le montre, sources à l’appui.',
			pills: ['Tout est sourcé', 'Les mêmes règles pour tous', 'Vous pouvez nous corriger'],
			newToday: ['{n} nouvelle contradiction aujourd’hui', '{n} nouvelles contradictions aujourd’hui'],
			newWeek: ['{n} nouvelle contradiction cette semaine', '{n} nouvelles contradictions cette semaine'],
			featuredLabel: 'À la une',
			latestTitle: 'Les dernières contradictions',
			latestAll: 'Tout le fil',
			scoreTitle: 'Le Tartuffe-Score',
			scoreBody: 'Il mesure les contradictions d’un élu avec lui-même, entre ce qu’il dit, promet et vote, et avec la justice : nier ce qu’un tribunal a établi compte aussi. On mesure la cohérence, pas la vérité.',
			podiumTitle: 'Le podium',
			podiumSub: 'Les plus tartuffes parmi les candidats',
			labShort: 'Le laboratoire indépendant derrière tartuffe.me conçoit, finance et teste des outils pour une démocratie plus transparente.',
			evShort: 'Classez jusqu’à trois candidats ou votez blanc, avec votre passeport. Personne, ni nous ni l’État, ne peut savoir qui a voté quoi, et chacun peut recompter.',
			evDiscover: 'Découvrir le vote',
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
			feedCta: 'Toutes les contradictions',
			feedNote: 'Le fil : les contradictions des candidats, les plus récentes et les plus remarquées.',
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
			howMore: 'La méthode en détail',
			exNote: 'On mesure la cohérence, pas la vérité.',

			svcKicker: 'Suivre et vérifier',
			svcTitle: 'Tout est en accès libre',
			services: [
				{ href: '/dashboard', icon: 'rank', title: 'Le classement', body: 'Chaque élu et chaque parti, de cohérent à tartuffe. Cherchez un nom, comparez.', cta: 'Ouvrir' },
				{ href: '/feed', icon: 'feed', title: 'Le fil', body: 'Les dernières contradictions, au fil de l’eau. Un +1 les fait remonter.', cta: 'Lire' },
				{ href: CODE, icon: 'code', title: 'Le code', body: 'Le site est open source : la collecte, la détection, le calcul du score. Lisez-le, vérifiez-le, corrigez-le.', cta: 'Voir le code' }
			],
			aboutLink: 'Comment le score est calculé',
			podiumKicker: 'Le podium',
			podiumLink: 'Tout le classement',

			evTag: 'Expérience temporaire de Lanterne Labs',
			evTitle: 'Présidentielle 2027 : voter en ligne, vraiment en secret',
			evBody: 'Classez jusqu’à trois candidats ou votez blanc, avec votre passeport. Personne, ni nous ni l’État, ne peut savoir qui a voté quoi, et chacun peut recompter. Le Tartuffe-Score de chaque candidat s’affiche à côté.',
			evEnds: 'Jusqu’au {d}',
			evCta: 'Voter',
			evMore: 'Comment ça marche',

			labKicker: 'Lanterne Labs',
			labTitle: 'Le laboratoire indépendant derrière tartuffe.me',
			labBody: 'Lanterne Labs conçoit, finance et teste des outils pour une démocratie plus transparente. Les nouvelles technologies permettent enfin à chacun de suivre ce que disent et font les élus, sources à l’appui, de façon lisible. tartuffe.me, le détecteur de contradictions, est son premier produit, le vote secret en ligne sa première expérience.',
			labCta: 'Découvrir Lanterne Labs'
		},
		en: {
			title: 'Coherent or tartuffe?',
			lead: 'What politicians say, how they vote, what the courts ruled. When it does not add up, we show it, with the sources.',
			pills: ['100 % sourced', 'Same rules for all', 'Corrected by you'],
			newToday: ['{n} new contradiction today', '{n} new contradictions today'],
			newWeek: ['{n} new contradiction this week', '{n} new contradictions this week'],
			featuredLabel: 'Top story',
			latestTitle: 'The latest contradictions',
			latestAll: 'The whole feed',
			scoreTitle: 'The Tartuffe-Score',
			scoreBody: 'It measures a politician’s contradictions with themselves, between what they say, promise and vote, and with the courts: denying what a court established counts too. Consistency, not truth.',
			podiumTitle: 'The podium',
			podiumSub: 'The most tartuffe among the candidates',
			labShort: 'The independent lab behind tartuffe.me builds, funds and tests tools for a more transparent democracy.',
			evShort: 'Rank up to three candidates or vote blank, with your passport. Nobody, neither we nor the State, can know who voted what, and anyone can recount.',
			evDiscover: 'Discover the vote',
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
			feedCta: 'All the contradictions',
			feedNote: 'The feed: the candidates’ contradictions, the latest and the most noticed.',
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
			howMore: 'The method in detail',
			exNote: 'Consistency, not truth.',

			svcKicker: 'Follow and verify',
			svcTitle: 'Everything is public',
			services: [
				{ href: '/dashboard', icon: 'rank', title: 'The ranking', body: 'Every politician and party, from coherent to tartuffe. Search, compare.', cta: 'Open' },
				{ href: '/feed', icon: 'feed', title: 'The feed', body: 'The latest contradictions, live. A +1 pushes them up.', cta: 'Read' },
				{ href: CODE, icon: 'code', title: 'The code', body: 'The site is open source: the collection, the detection, the score. Read it, check it, fix it.', cta: 'See the code' }
			],
			aboutLink: 'How the score is computed',
			podiumKicker: 'The podium',
			podiumLink: 'The whole ranking',

			evTag: 'A temporary experiment by Lanterne Labs',
			evTitle: 'Presidential 2027: online voting, truly private',
			evBody: 'Rank up to 3 candidates, or vote blank, with your passport. Nobody, neither we nor the State, can know who voted what, and anyone can recount. Each candidate’s Tartuffe-Score is right beside.',
			evEnds: 'Until {d}',
			evCta: 'Vote',
			evMore: 'How it works',

			labKicker: 'Lanterne Labs',
			labTitle: 'The independent lab behind tartuffe.me',
			labBody: 'Lanterne Labs builds, funds and tests tools for a more transparent democracy. New technology finally lets anyone follow what elected officials say and do, with sources, in a readable way. tartuffe.me, the contradiction checker, is its first product; the private vote, its first experiment.',
			labCta: 'Discover Lanterne Labs'
		}
	};
	let c = $derived(content[currentLang]);
	let freshText = $derived(fresh ? (fresh.week ? c.newWeek : c.newToday)[fresh.n > 1 ? 1 : 0].replace('{n}', String(fresh.n)) : '');
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
		<path d="M8 7l-5 5 5 5M16 7l5 5-5 5M13.5 5l-3 14" />
	{/if}
{/snippet}

<article class="landing">
	<header class="hero">
		<div class="hero-text">
			<p class="hero-day rise1">
				<span class="live-dot"></span>
				<span>{today}</span>
				{#if freshText}<a class="tag-red" href="/feed?sort=new">{freshText}</a>{/if}
			</p>
			<h1 class="rise1">{c.title}</h1>
			<p class="lead rise2">{c.lead}</p>
			<div class="ctas rise2">
				<a class="pill-btn pill-btn--red" href="/dashboard">{c.ctaMain}</a>
				<a class="pill-btn pill-btn--ghost" href="/feed">{c.ctaFeed}</a>
			</div>
			<ul class="trust rise2">
				{#each c.pills as p}
					<li><svg viewBox="0 0 16 16" aria-hidden="true"><path d="m3.5 8.5 3 3 6-7" /></svg>{p}</li>
				{/each}
			</ul>
		</div>
		<div class="hero-card rise3">
			<p class="label">{c.featuredLabel}</p>
			{#if clash}
				<ClashCard bind:item={clash} lang={currentLang} feature />
			{:else if !loaded}
				<div class="card-skeleton skeleton"></div>
			{/if}
		</div>
	</header>

	<!-- The three ways in, right under the hero. -->
	<section class="ways" aria-label={c.svcTitle}>
		{#each c.services as s (s.href)}
			<a href={s.href} target={s.href.startsWith('http') ? '_blank' : undefined} rel={s.href.startsWith('http') ? 'noopener noreferrer' : undefined}>
				<svg viewBox="0 0 24 24" aria-hidden="true">{@render icon(s.icon)}</svg>
				<span><strong>{s.title}</strong><small>{s.body}</small></span>
			</a>
		{/each}
	</section>

	{#if latest.length > 0}
		<section class="block">
			<div class="sec-head">
				<h2><span class="live-dot"></span>{c.latestTitle}</h2>
				<a class="link" href="/feed?sort=new">{c.latestAll}</a>
			</div>
			<div class="latest">
				{#each latest as _, i (latest[i].id)}
					<ClashCard bind:item={latest[i]} lang={currentLang} />
				{/each}
			</div>
		</section>
	{/if}

	<section class="block podium">
		<div class="podium-card">
			<Podium lang={currentLang} />
		</div>
		<p class="center"><a class="link" href="/dashboard">{c.podiumLink}</a></p>
	</section>

	<section class="block">
		<div class="method">
			<div>
				<h2>{c.scoreTitle}</h2>
				<p class="method-body">{c.scoreBody}</p>
				<div class="scale" aria-hidden="true">
					<span class="scale-bar"></span>
					<span class="scale-ends"><span>0 · {c.scoreLow}</span><span>{c.scoreHigh} · 100</span></span>
				</div>
				<a class="link" href="/about#how">{c.howMore}</a>
			</div>
			<ol class="steps">
				{#each c.steps as [t, d], i (i)}
					<li><strong>{t}</strong><span>{d}</span></li>
				{/each}
			</ol>
		</div>
	</section>

	<section class="block duo">
		<div class="event">
			<div class="ev-head">
				<span class="ev-tag">{c.evTag}</span>
				{#if featured?.closes_at}
					<span class="ev-ends">{c.evEnds.replace('{d}', dateText(featured.closes_at))}</span>
				{/if}
			</div>
			<h2>{c.evTitle}</h2>
			<p>{c.evShort}</p>
			<div class="ev-actions">
				<!-- The vote's presentation first, never the ballot: a voter learns
				     what the vote is and why it asks for a passport before they vote. -->
				<a class="pill-btn pill-btn--blue pill-btn--sm" href={featured ? eventPaths(featured.slug).intro : POLL.intro}>{c.evDiscover}</a>
				<a class="ev-more" href={featured ? eventPaths(featured.slug).how : POLL.how}>{c.evMore}</a>
			</div>
		</div>
		<a class="lab" href="/lab">
			<h2>{c.labKicker}</h2>
			<p>{c.labShort}</p>
			<span class="lab-cta">{c.labCta}</span>
		</a>
	</section>
</article>

<style>
	.landing {
		text-align: left;
	}
	.landing h1,
	.landing h2 {
		text-align: left;
	}
	/* ---------- Hero: the claim on the left, a contradiction on the right ---------- */
	.hero {
		display: grid;
		grid-template-columns: minmax(0, 1.05fr) minmax(0, 0.95fr);
		gap: 3rem;
		align-items: center;
		padding: 1.5rem 0 0;
	}
	.hero-day {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem 0.7rem;
		margin-bottom: 1.4rem;
		font-size: 0.88rem;
		color: var(--text-secondary);
	}
	.tag-red {
		display: inline-flex;
		align-items: center;
		padding: 0.15rem 0.6rem;
		border-radius: var(--radius-full);
		background: rgba(229, 56, 79, 0.12);
		color: var(--accent-hover);
		font-size: 0.78rem;
		font-weight: 600;
	}
	.tag-red:hover {
		background: rgba(229, 56, 79, 0.2);
	}
	.hero h1 {
		margin-bottom: 1.4rem;
		font-size: clamp(3rem, 7vw, 5.6rem);
		font-weight: 820;
		letter-spacing: -0.045em;
		line-height: 0.92;
		text-wrap: balance;
	}
	.lead {
		margin: 0 0 2rem;
		max-width: 36rem;
		font-size: 1.18rem;
		line-height: 1.55;
		color: var(--text-secondary);
		text-align: left;
		text-wrap: pretty;
	}
	.ctas {
		display: flex;
		flex-wrap: wrap;
		gap: 0.7rem;
	}
	.trust {
		display: flex;
		flex-wrap: wrap;
		gap: 0.4rem 1.1rem;
		margin-top: 1.8rem;
		list-style: none;
		font-size: 0.84rem;
		color: var(--text-muted);
	}
	.trust li {
		display: inline-flex;
		align-items: center;
		gap: 0.35rem;
	}
	.trust svg {
		width: 0.9rem;
		height: 0.9rem;
		fill: none;
		stroke: var(--green);
		stroke-width: 2;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.hero-card {
		display: grid;
		gap: 0.6rem;
		min-width: 0;
	}
	.label {
		font-size: 0.85rem;
		font-weight: 600;
		color: var(--text-secondary);
	}
	.card-skeleton {
		height: 22rem;
		border-radius: var(--radius-lg);
	}
	@keyframes rise {
		from {
			opacity: 0;
			transform: translateY(14px);
		}
	}
	.rise1 { animation: rise 0.7s var(--ease) both; }
	.rise2 { animation: rise 0.7s var(--ease) 0.08s both; }
	.rise3 { animation: rise 0.8s var(--ease) 0.16s both; }
	@media (prefers-reduced-motion: reduce) {
		.rise1, .rise2, .rise3 { animation: none; }
	}

	/* ---------- The three ways in ---------- */
	.ways {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 0.9rem;
		margin-top: 3.5rem;
	}
	.ways a {
		display: flex;
		gap: 0.9rem;
		padding: 1.2rem;
		border-radius: var(--radius-lg);
		border: 1px solid var(--border-card);
		color: var(--text-primary);
		transition: border-color 0.2s, background 0.2s, transform 0.25s var(--ease);
	}
	.ways a:hover {
		border-color: var(--border-card-hover);
		background: var(--bg-card);
		transform: translateY(-2px);
	}
	.ways svg {
		flex-shrink: 0;
		width: 1.6rem;
		height: 1.6rem;
		fill: none;
		stroke: var(--accent-hover);
		stroke-width: 1.8;
		stroke-linecap: round;
		stroke-linejoin: round;
	}
	.ways span {
		display: grid;
		gap: 0.25rem;
	}
	.ways small {
		font-size: 0.86rem;
		line-height: 1.45;
		color: var(--text-secondary);
	}

	/* ---------- Sections ---------- */
	.block {
		margin-top: 4.5rem;
	}
	.sec-head {
		display: flex;
		align-items: flex-end;
		justify-content: space-between;
		gap: 1rem;
		margin-bottom: 1.4rem;
	}
	.block h2 {
		font-size: clamp(1.6rem, 3.2vw, 2.3rem);
		font-weight: 760;
		letter-spacing: -0.035em;
		line-height: 1.08;
	}
	.sec-head h2 {
		display: flex;
		align-items: center;
		gap: 0.75rem;
	}
	.link {
		font-weight: 600;
		font-size: 0.92rem;
		color: var(--text-secondary);
		border-bottom: 1px solid var(--border-card-hover);
		padding-bottom: 0.1rem;
		white-space: nowrap;
	}
	.link:hover {
		color: var(--text-primary);
		border-color: var(--text-primary);
	}
	.center {
		text-align: center;
		margin-top: 1.4rem;
	}
	.latest {
		display: grid;
		grid-template-columns: repeat(3, minmax(0, 1fr));
		gap: 1rem;
		align-items: start;
	}

	/* ---------- Podium ---------- */
	.podium-card {
		max-width: 46rem;
		margin: 0 auto;
		padding: 1.6rem 1.6rem 0.5rem;
		border-radius: var(--radius-lg);
		border: 1px solid var(--border-card);
		background: var(--bg-card);
		box-shadow: var(--shadow-card);
	}
	/* ---------- The score, in a few lines ---------- */
	.method {
		display: grid;
		grid-template-columns: minmax(0, 1.1fr) minmax(0, 0.9fr);
		gap: 2.5rem;
		align-items: center;
		padding: 2.5rem;
		border-radius: 1.75rem;
		border: 1px solid var(--border-card);
		background: radial-gradient(90% 120% at 0% 0%, rgba(242, 182, 64, 0.07), transparent 60%), var(--bg-card);
	}
	.method-body {
		margin: 1rem 0 1.4rem;
		color: var(--text-secondary);
		line-height: 1.6;
	}
	.scale {
		margin-bottom: 1.4rem;
	}
	.scale-bar {
		display: block;
		height: 0.45rem;
		border-radius: var(--radius-full);
		background: linear-gradient(90deg, var(--green), var(--amber), var(--red));
	}
	.scale-ends {
		display: flex;
		justify-content: space-between;
		margin-top: 0.4rem;
		font-size: 0.78rem;
		font-weight: 600;
		color: var(--text-muted);
	}
	.steps {
		display: grid;
		gap: 0.6rem;
		list-style: none;
		counter-reset: step;
	}
	.steps li {
		counter-increment: step;
		display: grid;
		grid-template-columns: 2.4rem 1fr;
		column-gap: 0.9rem;
		padding: 0.9rem 1rem;
		border-radius: 1rem;
		background: rgba(0, 0, 0, 0.22);
		border: 1px solid var(--border-card);
	}
	.steps li::before {
		content: counter(step);
		grid-row: span 2;
		align-self: center;
		font-family: var(--font-num);
		font-weight: 800;
		font-size: 2.2rem;
		line-height: 1;
		color: var(--gold);
		text-align: center;
	}
	.steps span {
		font-size: 0.9rem;
		color: var(--text-secondary);
	}

	/* ---------- The event and the lab ---------- */
	.duo {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 1rem;
	}
	.event,
	.lab {
		display: flex;
		flex-direction: column;
		gap: 0.8rem;
		padding: 1.8rem;
		border-radius: 1.5rem;
	}
	.event {
		background: radial-gradient(100% 120% at 100% 0%, rgba(142, 170, 240, 0.17), transparent 60%), var(--bg-card);
		border: 1px solid rgba(142, 170, 240, 0.32);
	}
	.lab {
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		color: var(--text-primary);
		transition: border-color 0.2s, transform 0.25s var(--ease);
	}
	.lab:hover {
		border-color: var(--border-card-hover);
		transform: translateY(-2px);
	}
	.duo h2 {
		font-size: 1.7rem;
		font-weight: 700;
		letter-spacing: -0.03em;
		line-height: 1.15;
	}
	.duo p {
		color: var(--text-secondary);
		line-height: 1.6;
	}
	.ev-head {
		display: flex;
		flex-wrap: wrap;
		justify-content: space-between;
		gap: 0.5rem 1rem;
		font-size: 0.82rem;
	}
	.ev-tag {
		font-weight: 600;
		color: var(--event);
	}
	.ev-ends {
		color: var(--text-secondary);
	}
	.ev-actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.75rem 1.25rem;
		margin-top: auto;
		padding-top: 0.4rem;
	}
	.ev-more {
		font-size: 0.92rem;
		color: var(--text-secondary);
	}
	.ev-more:hover {
		color: var(--text-primary);
	}
	.lab-cta {
		margin-top: auto;
		font-weight: 600;
		font-size: 0.92rem;
		color: var(--accent-hover);
	}

	@media (max-width: 1000px) {
		.hero {
			grid-template-columns: 1fr;
			gap: 2.2rem;
		}
		.latest {
			grid-template-columns: 1fr;
		}
		.method {
			grid-template-columns: 1fr;
			padding: 1.6rem;
		}
	}
	@media (max-width: 720px) {
		.ways,
		.duo {
			grid-template-columns: 1fr;
		}
		/* The podium needs the whole width of a phone for its three names. */
		.podium-card {
			padding: 0;
			border: 0;
			background: none;
			box-shadow: none;
		}
		.block {
			margin-top: 3rem;
		}
		.sec-head {
			flex-direction: column;
			align-items: flex-start;
		}
	}
</style>
