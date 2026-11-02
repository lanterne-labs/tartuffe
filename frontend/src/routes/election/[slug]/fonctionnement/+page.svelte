<script lang="ts">
	// How the vote works, for everyone: four ideas, each with its short
	// animation (the passport stays on the phone, the ballot never comes to
	// our server, only the result is revealed, anyone checks every step),
	// and the limits said plainly. The
	// technical version lives with the code (isoloir, docs/comment-ca-marche.md).
	// Every line must stay true to isoloir (DESIGN.md) and to the app.
	import { page } from '$app/stores';
	import { lang, type Lang } from '$lib/i18n';
	import { eventPaths } from '$lib/event';
	import ZKProofAnimation from '$lib/components/ZKProofAnimation.svelte';
	import BallotPathAnimation from '$lib/components/vote/BallotPathAnimation.svelte';
	import CountAnimation from '$lib/components/vote/CountAnimation.svelte';
	import AuditLoop from '$lib/components/vote/AuditLoop.svelte';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));
	let paths = $derived(eventPaths($page.params.slug ?? ''));

	const TECH = 'https://github.com/tartuffeme/isoloir/blob/main/docs/comment-ca-marche.md';

	const content = {
		fr: {
			title: 'Votre vote reste secret. Même pour nous.',
			lead: 'Quatre idées simples, et chacun peut vérifier qu’elles tiennent.',
			passport: 'Il prouve seulement que vous êtes majeur et français, une seule fois. Un passeport, une voix.',
			ballotTitle: 'Votre bulletin ne passe pas par nous',
			ballot: 'Il part scellé vers des relais publics, tenus par d’autres. Nous allons l’y chercher, au milieu des autres : nous ne voyons jamais d’où il vient.',
			countTitle: 'Seul le résultat est révélé',
			count: 'Les bulletins sont comptés sans être ouverts. Personne ne lit le vôtre : seul le résultat est révélé, avec une preuve que chacun peut contrôler.',
			auditTitle: 'Chacun peut tout recompter',
			audit: 'Tout est inscrit dans un registre public, signé et daté dans Bitcoin. Votre navigateur peut refaire chaque contrôle, de l’inscription au résultat.',
			auditCta: 'Vérifier maintenant',
			limitsTitle: 'Les limites, franchement',
			limits: [
				'La clé qui révèle le résultat pourrait aussi ouvrir un bulletin seul, sans savoir de qui il vient. Nous publions seulement les résultats, avec leurs preuves.',
				'Vous pouvez montrer votre vote à quelqu’un en lui donnant votre clé : l’achat de votes n’est pas empêché.',
				'Il faut un smartphone NFC et un passeport à puce.',
				'C’est une expérience de tartuffe.me : ni un scrutin officiel, ni un sondage représentatif.'
			],
			tech: 'La version technique',
			vote: 'Aller voter'
		},
		en: {
			title: 'Your vote stays secret. Even from us.',
			lead: 'Four simple ideas, and anyone can check they hold.',
			passport: 'It only proves you are an adult French national, once. One passport, one vote.',
			ballotTitle: 'Your ballot never goes through us',
			ballot: 'It leaves sealed for public relays run by others. We fetch it there, among the rest: we never see where it came from.',
			countTitle: 'Only the result is revealed',
			count: 'Ballots are counted without being opened. No one reads yours: only the result is revealed, with a proof anyone can check.',
			auditTitle: 'Anyone can recount everything',
			audit: 'Everything goes into a public log, signed and dated in Bitcoin. Your browser can redo every check, from registration to result.',
			auditCta: 'Verify now',
			limitsTitle: 'The limits, frankly',
			limits: [
				'The key that reveals the result could also open a single ballot, without knowing whose it is. We only publish results, with their proofs.',
				'You can show your vote to someone by giving them your key: vote buying is not prevented.',
				'You need an NFC smartphone and a chip passport.',
				'It is an experiment by tartuffe.me: neither an official election nor a representative poll.'
			],
			tech: 'The technical version',
			vote: 'Go vote'
		}
	};
	let c = $derived(content[currentLang]);
</script>

<article class="page-content how">
	<header class="hero">
		<h1>{c.title}</h1>
		<p class="lead">{c.lead}</p>
	</header>

	<section class="idea">
		<span class="num">1</span>
		<ZKProofAnimation lang={currentLang} />
		<p>{c.passport}</p>
	</section>

	<section class="idea">
		<span class="num">2</span>
		<h2>{c.ballotTitle}</h2>
		<BallotPathAnimation lang={currentLang} />
		<p>{c.ballot}</p>
	</section>

	<section class="idea">
		<span class="num">3</span>
		<h2>{c.countTitle}</h2>
		<CountAnimation lang={currentLang} />
		<p>{c.count}</p>
	</section>

	<section class="idea">
		<span class="num">4</span>
		<h2>{c.auditTitle}</h2>
		<div class="loop"><AuditLoop lang={currentLang} /></div>
		<p>{c.audit}</p>
		<a class="btn btn--primary" href={paths.verify}>{c.auditCta}</a>
	</section>


	<details class="limits">
		<summary>{c.limitsTitle}</summary>
		<ul>{#each c.limits as l (l)}<li>{l}</li>{/each}</ul>
	</details>

	<p class="end">
		<a class="btn btn--ghost" href={paths.vote}>{c.vote}</a>
		<a class="tech" href={TECH} target="_blank" rel="noopener">{c.tech} ↗</a>
	</p>
</article>

<style>
	.how {
		max-width: 38rem;
		margin: 0 auto;
		text-align: center;
	}
	.hero {
		margin: 1.5rem 0 2rem;
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(1.9rem, 6vw, 2.8rem);
		font-weight: 600;
		line-height: 1.08;
		margin: 0 auto 0.7rem;
		text-wrap: balance;
	}
	.lead {
		font-size: 1.05rem;
		color: var(--text-secondary);
		text-wrap: balance;
		margin: 0;
	}
	.idea {
		position: relative;
		margin: 0 0 1.25rem;
		padding: 2rem 1.25rem 1.4rem;
		border-radius: var(--radius-lg, 18px);
		background: var(--bg-card);
		border: 1px solid var(--border-card, var(--glass-border));
	}
	.num {
		position: absolute;
		top: -0.9rem;
		left: 50%;
		transform: translateX(-50%);
		width: 1.8rem;
		height: 1.8rem;
		border-radius: 50%;
		display: grid;
		place-items: center;
		background: var(--accent);
		color: #fff;
		font-weight: 800;
		font-size: 0.9rem;
	}
	.idea h2 {
		font-size: 1.25rem;
		font-weight: 800;
		line-height: 1.2;
		margin: 0 0 0.9rem;
		text-wrap: balance;
	}
	.idea p {
		margin: 0.9rem auto 0;
		color: var(--text-secondary);
		line-height: 1.55;
		max-width: 30rem;
		text-wrap: balance;
	}
	.loop {
		margin: 0.4rem 0 0;
	}
	.btn {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		min-height: 2.9rem;
		padding: 0 1.5rem;
		border-radius: var(--radius-full, 999px);
		font-weight: 700;
		text-decoration: none;
	}
	.btn--primary {
		margin-top: 1rem;
		background: var(--accent);
		color: #fff;
	}
	.btn--primary:hover {
		background: var(--accent-hover);
	}
	.btn--ghost {
		border: 1px solid var(--glass-border);
		color: var(--text-primary);
	}
	@media (min-width: 640px) {
		}
	.limits {
		margin: 0.8rem 0 1.5rem;
		padding: 0.8rem 1.1rem;
		border-radius: var(--radius-md);
		background: var(--bg-card);
	}
	.limits summary {
		cursor: pointer;
		font-weight: 700;
	}
	.limits ul {
		text-align: left;
		margin: 0.8rem 0 0.2rem;
		padding-left: 1.1rem;
		display: grid;
		gap: 0.5rem;
		font-size: 0.9rem;
		color: var(--text-secondary);
		line-height: 1.5;
	}
	.end {
		display: flex;
		flex-direction: column;
		align-items: center;
		gap: 0.9rem;
		margin: 1.5rem 0 1rem;
	}
	.tech {
		font-size: 0.85rem;
		color: var(--text-secondary);
	}
</style>
