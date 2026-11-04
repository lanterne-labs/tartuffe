<script lang="ts">
	// How to reach us: a reader who found an error, a person named on the site
	// (the right of reply), the press, and a request about personal data. Every
	// subject has its own mail subject so the replies can be sorted.
	import { lang, type Lang } from '$lib/i18n';
	import { CONTACT_EMAIL, mailto } from '$lib/contact';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	type Topic = { title: string; body: string; subject: string };
	const content: Record<Lang, { kicker: string; title: string; lead: string; topics: Topic[]; write: string; soon: string; report: string; reportLink: string }> = {
		fr: {
			kicker: 'Contact',
			title: 'Nous écrire',
			lead: 'Une seule adresse, lue par des humains. Précisez le sujet : nous répondons d’abord aux demandes de correction et aux droits de réponse.',
			topics: [
				{ title: 'Une erreur', body: 'Un fait mal rapporté, une date fausse, une contradiction qui n’en est pas une. Donnez le lien de la page : nous vérifions, et chaque retrait est publié sur la page des corrections.', subject: 'Erreur' },
				{ title: 'Droit de réponse', body: 'Vous êtes une personne citée sur le site, ou vous la représentez ? Écrivez-nous : nous publions votre réponse sous l’élément concerné, et nous corrigeons ce qui doit l’être.', subject: 'Droit de réponse' },
				{ title: 'Presse et partenariats', body: 'Une question sur la méthode, les données ou le vote en ligne.', subject: 'Presse' },
				{ title: 'Vos données', body: 'Nous gardons très peu sur vous : l’empreinte de votre compte, votre pseudonyme, vos +1 et vos signalements. Votre profil supprime votre compte d’un clic. Une question ?', subject: 'Données personnelles' }
			],
			write: 'Écrire',
			soon: 'L’adresse de contact arrive dans quelques jours.',
			report: 'Pour un fait ou une contradiction précis, le plus rapide reste le bouton « Signaler » de l’élément.',
			reportLink: 'Voir les corrections publiées'
		},
		en: {
			kicker: 'Contact',
			title: 'Write to us',
			lead: 'One address, read by humans. Say what it is about: corrections and replies come first.',
			topics: [
				{ title: 'An error', body: 'A misreported fact, a wrong date, a contradiction that isn’t one. Send the link of the page: we check, and every removal is published on the corrections page.', subject: 'Error' },
				{ title: 'Right of reply', body: 'You are a person named on the site, or you represent them? Write to us: we publish your reply under the item, and correct what needs correcting.', subject: 'Right of reply' },
				{ title: 'Press and partnerships', body: 'A question about the method, the data or the online vote.', subject: 'Press' },
				{ title: 'Your data', body: 'We keep very little about you: a fingerprint of your account, your pseudonym, your +1s and your reports. Your profile deletes your account in one click. A question?', subject: 'Personal data' }
			],
			write: 'Write',
			soon: 'The contact address is coming in a few days.',
			report: 'For one specific fact or contradiction, the quickest is its “Report” button.',
			reportLink: 'See the published corrections'
		}
	};
	let c = $derived(content[currentLang]);
</script>

<article class="legal">
	<span class="kicker">{c.kicker}</span>
	<h1>{c.title}</h1>
	<p class="lead">{c.lead}</p>

	{#if CONTACT_EMAIL}
		<p class="address"><a href="mailto:{CONTACT_EMAIL}">{CONTACT_EMAIL}</a></p>
	{:else}
		<p class="address soon">{c.soon}</p>
	{/if}

	<div class="topics">
		{#each c.topics as t (t.subject)}
			<section class="topic">
				<h2>{t.title}</h2>
				<p>{t.body}</p>
				{#if CONTACT_EMAIL}
					<a class="write" href={mailto(t.subject)}>{c.write} →</a>
				{/if}
			</section>
		{/each}
	</div>

	<p class="note">{c.report} <a href="/corrections">{c.reportLink}</a></p>
</article>

<style>
	.legal { max-width: 760px; margin: 0 auto; padding: 1rem 0 3rem; }
	.kicker { display: block; text-align: center; font-size: 0.75rem; letter-spacing: 0.08em; text-transform: uppercase; color: var(--text-muted); }
	h1 { font-size: 2rem; margin: 0.25rem 0 0.75rem; }
	.lead { color: var(--text-secondary); line-height: 1.6; }
	.address { margin: 1.5rem 0; text-align: center; font-size: 1.125rem; font-weight: 600; }
	.address.soon { font-weight: 400; color: var(--text-muted); font-style: italic; }
	.topics { display: grid; grid-template-columns: repeat(auto-fit, minmax(260px, 1fr)); gap: 1rem; margin: 1.5rem 0; }
	.topic { background: var(--bg-card); border: 1px solid var(--border-card); border-radius: var(--radius-md); padding: 1rem 1.25rem; }
	.topic h2 { font-size: 1rem; margin-bottom: 0.5rem; }
	.topic p { color: var(--text-secondary); font-size: 0.9rem; line-height: 1.55; }
	.write { display: inline-block; margin-top: 0.75rem; font-size: 0.875rem; }
	.note { color: var(--text-muted); font-size: 0.875rem; }
</style>
