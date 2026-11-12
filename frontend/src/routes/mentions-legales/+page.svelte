<script lang="ts">
	// The legal notice (LCEN art. 6) and the terms of use. The publisher and the
	// contact address come from $lib/contact. The hosts must stay true to the
	// deploy: the site and its API on Railway, the vote app on GitHub Pages, the
	// photos on Cloudflare R2.
	import { lang, type Lang } from '$lib/i18n';
	import { CONTACT_EMAIL, PUBLISHER } from '$lib/contact';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	// A paragraph is a string, or pieces of text and links to the site's pages.
	type Para = string | (string | { href: string; text: string })[];
	type Section = { title: string; paras: Para[] };
	const hosts = [
		['Railway Corporation', '548 Market St PMB 68956, San Francisco, CA 94104, États-Unis — railway.com', 'le site et ses données'],
		['GitHub, Inc.', '88 Colin P. Kelly Jr. Street, San Francisco, CA 94107, États-Unis — github.com', 'l’application de vote (GitHub Pages)'],
		['Cloudflare, Inc.', '101 Townsend Street, San Francisco, CA 94107, États-Unis — cloudflare.com', 'les photos des responsables politiques']
	];
	const content: Record<Lang, { title: string; publisherTitle: string; publisherAnon: string; contactLabel: string; contactPage: string; hostsTitle: string; sections: Section[] }> = {
		fr: {
			title: 'Mentions légales et conditions d’utilisation',
			publisherTitle: 'Éditeur',
			// The hyphens of the article's number do not break (U+2011).
			publisherAnon: 'Ce site est édité à titre non professionnel par une personne physique qui, conformément à l’article 6‑III‑2 de la loi n° 2004-575 du 21 juin 2004 pour la confiance dans l’économie numérique, a communiqué ses éléments d’identification à l’hébergeur.',
			contactLabel: 'Contact',
			contactPage: 'la page Contact',
			hostsTitle: 'Hébergement',
			sections: [
				{ title: 'Ce qu’est le site', paras: [
					'tartuffe.me rapproche les déclarations publiques, les votes et les actes des responsables politiques, à partir de sources publiques, et signale les contradictions qu’il y trouve. Il mesure la cohérence d’une personne avec elle-même, pas la vérité de ses propos.',
					'Les faits sont extraits et les contradictions détectées automatiquement par des modèles d’intelligence artificielle, puis contrôlés par une seconde IA et, sur échantillon, par l’équipe. Malgré ces contrôles, une erreur reste possible : chaque fait renvoie à sa source, qui fait foi.'
				] },
				{ title: 'Corrections et droit de réponse', paras: [
					['Toute personne peut signaler un fait ou une contradiction depuis l’élément concerné ; les retraits sont publiés sur la ', { href: '/corrections', text: 'page Corrections' }, ', avec leur raison.'],
					'Une personne citée peut exercer son droit de réponse (art. 6-IV de la loi du 21 juin 2004) en écrivant à l’adresse de contact : la réponse est publiée sous l’élément concerné.'
				] },
				{ title: 'Le vote en ligne', paras: [
					'La consultation sur l’élection présidentielle est une expérience indépendante, en ligne, non représentative : ce n’est ni un sondage au sens de la loi n° 77-808 du 19 juillet 1977, ni un scrutin officiel. Ses résultats ne décrivent que les personnes qui y ont participé.'
				] },
				{ title: 'Sources et licences', paras: [
					'Données de l’Assemblée nationale (data.assemblee-nationale.fr) et discours publiés par vie-publique.fr (DILA), réutilisés sous la Licence Ouverte / Etalab 2.0 ; articles de presse cités en courte citation, avec le lien vers l’original.',
					'Les photos restent la propriété de leurs auteurs ; elles sont reproduites pour illustrer l’information. Un ayant droit qui souhaite le retrait d’une photo peut nous écrire.'
				] },
				{ title: 'Utilisation', paras: [
					'L’accès au site est libre et gratuit. Les contributions (signalements, « +1 ») doivent rester loyales : pas de contenu injurieux ni de signalements en masse. Nous pouvons retirer une contribution abusive.',
					['Le site est fourni tel quel, sans garantie d’exhaustivité. Les données personnelles sont traitées comme l’explique la ', { href: '/about/privacy', text: 'page Confidentialité' }, '.']
				] }
			]
		},
		en: {
			title: 'Legal notice and terms of use',
			publisherTitle: 'Publisher',
			publisherAnon: 'This site is published on a non-professional basis by a private individual who, under article 6‑III‑2 of French law no. 2004-575 of 21 June 2004, has given their identity to the host.',
			contactLabel: 'Contact',
			contactPage: 'the Contact page',
			hostsTitle: 'Hosting',
			sections: [
				{ title: 'What the site is', paras: [
					'tartuffe.me puts the public statements, votes and acts of politicians side by side, from public sources, and points out the contradictions it finds. It measures a person’s consistency with themselves, not the truth of what they say.',
					'Facts are extracted and contradictions detected automatically by AI models, then checked by a second AI and, on a sample, by the team. An error remains possible: every fact links to its source, which prevails.'
				] },
				{ title: 'Corrections and right of reply', paras: [
					['Anyone can report a fact or a contradiction from the item itself; removals are published on the ', { href: '/corrections', text: 'Corrections page' }, ' with their reason.'],
					'A person named on the site can exercise their right of reply by writing to the contact address: the reply is published under the item.'
				] },
				{ title: 'The online vote', paras: [
					'The presidential consultation is an independent, online, non-representative experiment: neither an opinion poll under French law no. 77-808 nor an official election. Its results only describe those who took part.'
				] },
				{ title: 'Sources and licences', paras: [
					'Data from the Assemblée nationale and speeches published by vie-publique.fr (DILA), reused under the Licence Ouverte / Etalab 2.0; press articles quoted briefly, with a link to the original.',
					'Photos remain the property of their authors and illustrate the information. A rights holder who wants a photo removed can write to us.'
				] },
				{ title: 'Use', paras: [
					'The site is free to use. Contributions (reports, “+1”) must be fair: no abuse, no mass reporting. We may remove an abusive contribution.',
					['The site is provided as is, with no guarantee of completeness. Personal data is handled as the ', { href: '/about/privacy', text: 'Privacy page' }, ' explains.']
				] }
			]
		}
	};
	let c = $derived(content[currentLang]);
</script>

<article class="legal">
	<h1>{c.title}</h1>

	<section>
		<h2>{c.publisherTitle}</h2>
		{#if PUBLISHER}
			<p>{PUBLISHER.name}{#if PUBLISHER.address}, {PUBLISHER.address}{/if}</p>
		{:else}
			<p>{c.publisherAnon}</p>
		{/if}
		<p class="contact-label">{c.contactLabel}&nbsp;:</p>
		<ul class="contact">
			<li><a href="mailto:{CONTACT_EMAIL}">{CONTACT_EMAIL}</a></li>
			<li><a href="/contact">{c.contactPage}</a></li>
		</ul>
	</section>

	<section>
		<h2>{c.hostsTitle}</h2>
		<ul>
			{#each hosts as [name, addr, what] (name)}
				<li><strong>{name}</strong>, {addr} ({what})</li>
			{/each}
		</ul>
	</section>

	{#each c.sections as s (s.title)}
		<section>
			<h2>{s.title}</h2>
			{#each s.paras as p}
				<p>
					{#if typeof p === 'string'}{p}{:else}{#each p as piece}{#if typeof piece === 'string'}{piece}{:else}<a href={piece.href}>{piece.text}</a>{/if}{/each}{/if}
				</p>
			{/each}
		</section>
	{/each}
</article>

<style>
	.legal { max-width: 760px; margin: 0 auto; padding: 1rem 0 3rem; line-height: 1.6; }
	h1 { font-family: var(--font-display); font-size: clamp(1.9rem, 5vw, 2.5rem); font-weight: 600; line-height: 1.15; margin-bottom: 1.5rem; }
	section { margin-bottom: 1.5rem; }
	h2 { font-size: 1.05rem; margin-bottom: 0.5rem; }
	p, li { color: var(--text-secondary); font-size: 0.925rem; margin-bottom: 0.5rem; }
	ul { padding-left: 1.25rem; }
	/* The two ways to write, one per line, each a 40px target. */
	.contact-label { margin-bottom: 0; }
	.contact { list-style: none; padding: 0; }
	.contact li { margin: 0; }
	.contact a { display: inline-flex; align-items: center; min-height: 2.5rem; }
</style>
