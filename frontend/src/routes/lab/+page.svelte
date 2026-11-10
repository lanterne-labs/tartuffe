<script lang="ts">
	// The lab behind the site: why it exists, and what it releases. The
	// contradiction checker is its permanent product; the presidential vote is
	// a temporary experiment, shown with its end date (the featured poll's).
	import { eventPaths } from '$lib/event';
	import { onMount } from 'svelte';
	import { lang, type Lang } from '$lib/i18n';
	import { API_BASE } from '$lib/config';
	import type { PollEvent } from '$lib/types';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

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

	const content = {
		fr: {
			kicker: 'Le labo',
			title: 'Un laboratoire pour une démocratie plus transparente',
			whyTitle: 'Pourquoi',
			why: [
				'La défiance envers les responsables politiques n’a jamais été aussi forte, et la désinformation va plus vite que les démentis.',
				'Les outils d’aujourd’hui permettent enfin de suivre ce que disent et font les élus, et de le rendre lisible par tous, sources à l’appui.',
				'Le labo invente et met à l’épreuve de nouvelles façons de donner ce pouvoir aux citoyens : comprendre, comparer, s’exprimer.'
			],
			mainTag: 'Produit permanent',
			mainTitle: 'Tartuffe, le détecteur de contradictions',
			mainBody: 'Tartuffe confronte ce que disent les responsables politiques à ce qu’ils votent, et signale quand les deux se contredisent. Chaque contradiction est sourcée, les règles sont les mêmes pour tous.',
			mainCta: 'Voir le classement',
			mainCta2: 'Comment le score est calculé',
			eventTag: 'Expérience temporaire',
			eventTitle: 'La présidentielle, par classement',
			eventEnds: 'Jusqu’au {d}.',
			eventBody: 'Classez jusqu’à 3 candidats et voyez, en direct, qui une majorité préfère à chacun des autres, et pas seulement qui arrive en tête. En ligne, et en toute confidentialité.',
			eventPoints: [
				'Le classement et le premier choix, côte à côte, mis à jour à chaque vote.',
				'Votre passeport prouve seulement que vous êtes majeur et français, depuis votre téléphone.',
				'Nous ne détenons rien qui vous identifie : même une fuite chez nous ne révélerait rien.',
				'Chacun peut recompter tous les bulletins.'
			],
			eventNote: 'Un vote d’opinion en ligne, distinct du scrutin officiel et des sondages : une preuve de faisabilité, en conditions réelles.',
			eventCta: 'Participer',
			eventHow: 'Comment ça marche',
			principlesTitle: 'Nos principes',
			principles: [
				['Vérifiable', 'Les sources sont citées, les méthodes publiées et les résultats recomptables.'],
				['Privé dès la conception', 'Ni publicité, ni traceur, ni e-mail.'],
				['Les mêmes règles pour tous', 'Chaque parti, chaque élu, traité de la même façon.'],
				['Lucide sur ses limites', 'Ce que nos outils garantissent, et ce qu’ils ne garantissent pas, est écrit noir sur blanc.']
			],
			next: 'D’autres expériences suivront.'
		},
		en: {
			kicker: 'The lab',
			title: 'A lab for more transparent democracies',
			whyTitle: 'Why',
			why: [
				'Distrust of politicians has never been higher, and disinformation travels faster than denials.',
				'Today’s tools finally make it possible to follow what elected officials say and do, and to make it readable by everyone, with sources.',
				'The lab invents and tests new ways to give that power to citizens: to understand, compare, speak up.'
			],
			mainTag: 'Permanent product',
			mainTitle: 'Tartuffe, the contradiction checker',
			mainBody: 'Tartuffe sets what politicians say against how they vote, and flags when the two clash. Every contradiction is sourced; the same rules apply to all.',
			mainCta: 'See the ranking',
			mainCta2: 'How the score is computed',
			eventTag: 'Temporary experiment',
			eventTitle: 'The presidential election, ranked',
			eventEnds: 'Until {d}.',
			eventBody: 'Rank up to 3 candidates and see, live, whom a majority prefers to each other one, not only who comes first. Online, and in full privacy.',
			eventPoints: [
				'The ranking and the first choices side by side, updated with every vote.',
				'Your passport only proves you are an adult French national, from your phone.',
				'We hold nothing that identifies you: even a leak on our side would reveal nothing.',
				'Anyone can recount every ballot.'
			],
			eventNote: 'An online opinion vote, distinct from the official election and from polls: a proof of feasibility, in real conditions.',
			eventCta: 'Take part',
			eventHow: 'How it works',
			principlesTitle: 'Our principles',
			principles: [
				['Verifiable', 'Sources cited, methods published, results recountable.'],
				['Private by design', 'No ads, no trackers, no e-mail.'],
				['Same rules for all', 'Every party, every politician, treated alike.'],
				['Honest about its limits', 'What our tools guarantee, and what they do not, is written down plainly.']
			],
			next: 'More experiments will follow.'
		}
	};
	let c = $derived(content[currentLang]);
</script>

<svelte:head>
	<title>{c.kicker} — tartuffe.me</title>
</svelte:head>

<article class="lab">
	<header class="hero">
		<h1>{c.title}</h1>
	</header>

	<section class="part">
		<h2>{c.whyTitle}</h2>
		<ul class="plain">
			{#each c.why as line}<li>{line}</li>{/each}
		</ul>
	</section>

	<section class="part">
		<div class="releases">
			<div class="release release--main">
				<span class="tag">{c.mainTag}</span>
				<h3>{c.mainTitle}</h3>
				<p>{c.mainBody}</p>
				<div class="actions">
					<a class="btn-main" href="/dashboard">{c.mainCta}</a>
					<a class="link" href="/about">{c.mainCta2} →</a>
				</div>
			</div>

			<div class="release release--event">
				<span class="tag tag--event">{c.eventTag}</span>
				<h3>{c.eventTitle}</h3>
				{#if featured?.closes_at}
					<p class="ends">{c.eventEnds.replace('{d}', dateText(featured.closes_at))}</p>
				{/if}
				<p>{c.eventBody}</p>
				<ul class="points">
					{#each c.eventPoints as line}<li>{line}</li>{/each}
				</ul>
				<p class="note">{c.eventNote}</p>
				<div class="actions">
					{#if featured}
						<a class="btn-event" href={eventPaths(featured.slug).intro}>{c.eventCta}</a>
					{/if}
					<a class="link" href={eventPaths(featured?.slug).how}>{c.eventHow} →</a>
				</div>
			</div>
		</div>
	</section>

	<section class="part">
		<h2>{c.principlesTitle}</h2>
		<div class="principles">
			{#each c.principles as [head, body]}
				<div><strong>{head}</strong><span>{body}</span></div>
			{/each}
		</div>
		<p class="next">{c.next}</p>
	</section>
</article>

<style>
	.lab {
		max-width: 52rem;
		margin: 1rem auto 3rem;
	}
	.hero {
		text-align: center;
		margin-bottom: 3rem;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(2.1rem, 6vw, 3.2rem);
		font-weight: 600;
		line-height: 1.08;
		margin: 0.4rem auto 1rem;
		max-width: 38rem;
		text-wrap: balance;
	}
	.part {
		margin-top: 2.75rem;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: 1.6rem;
		font-weight: 600;
		margin: 0 0 1rem;
	}
	h3 {
		font-family: var(--font-serif);
		font-size: 1.45rem;
		font-weight: 600;
		margin: 0.35rem 0 0.6rem;
		line-height: 1.15;
	}
	.plain {
		margin: 0;
		padding-left: 1.2rem;
		display: grid;
		gap: 0.6rem;
		color: var(--text-secondary);
		line-height: 1.6;
	}
	.releases {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 1rem;
	}
	.release {
		padding: 1.4rem 1.5rem;
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		border: 1px solid var(--border-card);
		display: flex;
		flex-direction: column;
	}
	.release--main {
		border-color: color-mix(in oklch, var(--accent) 60%, var(--border-card));
	}
	.release--event {
		border: 1px dashed var(--event);
		background: color-mix(in oklch, var(--event) 6%, var(--bg-card));
	}
	.release p {
		margin: 0 0 0.75rem;
		color: var(--text-secondary);
		line-height: 1.55;
		font-size: 0.95rem;
	}
	.release .ends {
		color: var(--text-primary);
		font-weight: 600;
	}
	.release .note {
		font-size: 0.85rem;
		font-style: italic;
	}
	.tag {
		align-self: flex-start;
		font-size: 0.75rem;
		font-weight: 700;
		letter-spacing: 0.1em;
		text-transform: uppercase;
		padding: 0.18rem 0.55rem;
		border-radius: var(--radius-full);
		background: var(--accent);
		color: white;
	}
	.tag--event {
		background: var(--event-strong);
	}
	.points {
		margin: 0 0 0.75rem;
		padding-left: 1.1rem;
		display: grid;
		gap: 0.4rem;
		font-size: 0.9rem;
		line-height: 1.5;
	}
	.actions {
		margin-top: auto;
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.6rem 1rem;
		padding-top: 0.5rem;
	}
	.btn-main,
	.btn-event {
		padding: 0.55rem 1.1rem;
		border-radius: var(--radius-full);
		color: white;
		font-weight: 600;
		font-size: 0.9rem;
		text-decoration: none;
	}
	.btn-main {
		background: var(--accent);
	}
	.btn-main:hover {
		background: var(--accent-hover);
	}
	.btn-event {
		background: var(--event-strong);
	}
	.btn-event:hover {
		background: var(--event);
	}
	.link {
		font-size: 0.88rem;
		color: var(--text-secondary);
	}
	/* A finger needs the text links as tall as the buttons beside them. */
	@media (pointer: coarse) {
		.link {
			display: inline-flex;
			align-items: center;
			min-height: 2.5rem;
			padding: 0 0.25rem;
		}
	}
	.principles {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.75rem;
	}
	.principles div {
		display: grid;
		gap: 0.25rem;
		padding: 1rem 1.1rem;
		border: 1px solid var(--border-card);
		border-radius: var(--radius-md);
		background: var(--bg-card);
	}
	.principles span {
		color: var(--text-secondary);
		font-size: 0.9rem;
		line-height: 1.5;
	}
	.next {
		margin: 1.5rem 0 0;
		color: var(--text-secondary);
	}
	@media (max-width: 720px) {
		.releases,
		.principles {
			grid-template-columns: minmax(0, 1fr);
		}
		.release {
			padding: 1.1rem;
		}
	}
</style>
