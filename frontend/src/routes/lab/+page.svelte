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
			lead: 'tartuffe.me conçoit des outils au service de la démocratie. Nous les construisons, les publions et les testons en conditions réelles : des outils qui obligent le pouvoir à rendre des comptes, et que chacun peut vérifier.',
			whyTitle: 'Pourquoi maintenant',
			why: [
				'La défiance envers les responsables politiques n’a jamais été aussi forte, et la désinformation va plus vite que les démentis.',
				'Les promesses s’oublient, les votes passent inaperçus, et les citoyens n’ont souvent que des opinions à opposer à des opinions.',
				'Notre réponse : des preuves. Ce que les élus disent et ce qu’ils font, confrontés publiquement, sources à l’appui ; des votes en ligne que chacun peut recompter, sans que personne ne sache qui a voté quoi.'
			],
			releasesTitle: 'Ce que le labo publie',
			mainTag: 'Produit permanent',
			mainTitle: 'Tartuffe, le détecteur de contradictions',
			mainBody: 'Tartuffe lit ce que disent les responsables politiques (discours, interviews, réseaux sociaux) et ce qu’ils font (leurs votes à l’Assemblée), puis signale quand les deux se contredisent. Chaque contradiction est sourcée, les règles sont les mêmes pour tous, et chacun peut signaler une erreur. Un score d’honnêteté résume la cohérence de chacun.',
			mainCta: 'Voir le classement',
			mainCta2: 'Comment le score est calculé',
			eventTag: 'Expérience temporaire',
			eventTitle: 'Voter en secret en ligne : la présidentielle',
			eventEnds: 'Jusqu’au {d}. L’expérience s’arrêtera ensuite.',
			eventBody: 'Une démonstration : peut-on voter en ligne en toute confidentialité, en s’appuyant sur ce que l’État fournit déjà, le passeport ou la carte d’identité à puce ?',
			eventPoints: [
				'Votre document prouve, sur votre téléphone, que vous êtes électeur, français et majeur : rien d’autre n’en sort.',
				'Votre bulletin est signé par une clé conservée sur vos appareils : ni nous, ni personne, ni l’État ne peut le relier à vous.',
				'Chaque bulletin et chaque total sont publiés dans un registre public : chacun peut recompter.',
				'Aucun compte, aucun e-mail, aucun numéro de téléphone.'
			],
			eventNote: 'Ce n’est ni un scrutin officiel ni un sondage représentatif, mais une preuve de faisabilité en conditions réelles.',
			eventCta: 'Participer',
			eventHow: 'Ce que nous voyons, et ce que nous ne voyons pas',
			principlesTitle: 'Nos principes',
			principles: [
				['Vérifiable', 'Les sources sont citées, les méthodes publiées et les résultats recomptables.'],
				['Privé dès la conception', 'Ni publicité, ni traceur, ni e-mail : nous ne savons pas qui vous êtes.'],
				['Les mêmes règles pour tous', 'Aucun parti, aucun élu ne bénéficie d’un traitement à part.'],
				['Lucide sur ses limites', 'Ce que nos outils ne garantissent pas est écrit noir sur blanc.']
			],
			next: 'D’autres expériences suivront.'
		},
		en: {
			kicker: 'The lab',
			title: 'A lab for more transparent democracies',
			lead: 'tartuffe.me is a research and development lab for the tools of democracy. We build, publish and test in real conditions tools that hold power to account, and that anyone can check.',
			whyTitle: 'Why now',
			why: [
				'Distrust of politicians has never been higher, and disinformation travels faster than denials.',
				'Promises are forgotten, votes go unnoticed, and citizens often have only opinions to set against opinions.',
				'The answer we stand for: evidence. What politicians say and do, compared in public, with the sources; online votes anyone can recount, without ever knowing who voted what.'
			],
			releasesTitle: 'What the lab releases',
			mainTag: 'Permanent product',
			mainTitle: 'Tartuffe, the contradiction checker',
			mainBody: 'Tartuffe reads what politicians say (speeches, interviews, social media) and what they do (their votes in Parliament), then flags when the two clash. Every contradiction is sourced, the same rules apply to all, and anyone can report an error. An honesty score sums up each one’s consistency.',
			mainCta: 'See the ranking',
			mainCta2: 'How the score is computed',
			eventTag: 'Temporary experiment',
			eventTitle: 'The private presidential vote',
			eventEnds: 'Until {d}, then the experiment ends.',
			eventBody: 'A demonstration: can we vote online in full privacy, relying on the State’s own infrastructure, the chip passport or ID card?',
			eventPoints: [
				'Your document proves, on your phone, that you are an adult French voter: nothing else leaves it.',
				'Your ballot is signed by a key kept on your devices: neither we, nor anyone, nor the State can tie it to you.',
				'Every ballot and every total go to a public log: anyone can recount.',
				'No account, no e-mail, no phone number.'
			],
			eventNote: 'It is neither an official election nor a representative poll: it is a proof of feasibility, in real conditions.',
			eventCta: 'Take part',
			eventHow: 'What we see, and what we don’t',
			principlesTitle: 'Our principles',
			principles: [
				['Verifiable', 'Sources cited, methods published, results recountable.'],
				['Private by design', 'No ads, no trackers, no e-mail: we don’t know who you are.'],
				['Same rules for all', 'No party, no politician is treated apart.'],
				['Honest about its limits', 'What our tools do not guarantee is written down, plainly.']
			],
			next: 'More experiments will follow.'
		}
	};
	let c = $derived(content[currentLang]);
</script>

<svelte:head>
	<title>{c.kicker} — tartuffe.me</title>
	<meta name="description" content={c.lead} />
</svelte:head>

<article class="lab">
	<header class="hero">
		<span class="kicker">{c.kicker}</span>
		<h1>{c.title}</h1>
		<p class="lead">{c.lead}</p>
	</header>

	<section class="part">
		<h2>{c.whyTitle}</h2>
		<ul class="plain">
			{#each c.why as line}<li>{line}</li>{/each}
		</ul>
	</section>

	<section class="part">
		<h2>{c.releasesTitle}</h2>
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
						<a class="btn-event" href={eventPaths(featured.slug).vote}>{c.eventCta}</a>
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
	.kicker {
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.12em;
		text-transform: uppercase;
		color: var(--accent-hover);
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
	.lead {
		font-size: 1.08rem;
		color: var(--text-secondary);
		line-height: 1.6;
		max-width: 38rem;
		margin: 0 auto;
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
		font-size: 0.68rem;
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
