<script lang="ts">
	// Privacy: what the site receives, keeps and never sees, feature by
	// feature (reading, the Google account, the voting key, the anonymous
	// ballots, the +1 and the reports, the session), and how to erase
	// everything. Every line must stay true to the code: backend/accounts,
	// backend/polls (and its semaphore package), lib/voter.ts, the access log
	// of backend/api, the fonts served by the site.
	import { lang, type Lang } from '$lib/i18n';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	type Part = { title: string; get: string[]; never: string[]; note?: string; link?: [string, string] };

	const content: Record<Lang, {
		kicker: string;
		title: string;
		lead: string;
		pills: string[];
		getLabel: string;
		neverLabel: string;
		parts: Part[];
		eraseTitle: string;
		erase: string[];
		promiseTitle: string;
		promises: string[];
		hosting: string;
		back: string;
	}> = {
		fr: {
			kicker: 'Confidentialité',
			title: 'Nous ne savons pas qui vous êtes.',
			lead: 'C’est voulu : un site qui juge les responsables politiques ne doit rien savoir de ses lecteurs ni de ses votants. Voici, point par point, ce que nous recevons, ce que nous gardons, et ce que nous ne voyons jamais.',
			pills: ['Pas de pub', 'Pas de traceur', 'Pas d’e-mail', 'Pas d’adresse IP gardée'],
			getLabel: 'Ce que nous recevons',
			neverLabel: 'Jamais',
			parts: [
				{
					title: 'Lire le site',
					get: [
						'Les pages demandées, comme tout site. Nos journaux notent la page, l’heure et la réponse, pas votre adresse IP.',
						'Votre adresse IP sert une minute, en mémoire, à freiner les abus (trop de requêtes), puis s’efface.'
					],
					never: [
						'Aucun cookie de suivi, aucune mesure d’audience, aucune publicité.',
						'Aucun script d’un autre site : même les polices de caractères sont servies par nous.'
					],
					note: 'Les photos des responsables politiques viennent de leurs sources publiques : votre navigateur les charge chez elles.'
				},
				{
					title: 'Se connecter avec Google',
					get: [
						'Un identifiant de votre compte Google : une suite de chiffres, sans votre nom ni votre e-mail.',
						'Nous ne le gardons même pas tel quel. Nous le passons dans une « moulinette » à sens unique, avec une clé secrète que nous seuls avons : il en sort une empreinte. Quand vous revenez, la même moulinette redonne la même empreinte, et nous vous reconnaissons. Mais de l’empreinte, impossible de revenir à l’identifiant : si notre base fuitait un jour, personne ne pourrait relier un compte du site à un compte Google.',
						'Le pseudo que vous choisissez, que vous seul voyez.'
					],
					never: [
						'Votre e-mail, votre nom, votre photo, vos contacts.',
						'Nous ne demandons à Google que le droit « openid », le plus petit qui existe : l’écran de Google le montre.'
					],
					note: 'Google sait que vous vous connectez à tartuffe.me, comme pour tout bouton « Continuer avec Google ».'
				},
				{
					title: 'Obtenir sa clé de vote',
					get: [
						'« Majeur : oui » et « nationalité française : oui », prouvés par l’app ZKPassport.',
						'Un identifiant de votre document propre à ce site, passé dans la même « moulinette » que l’identifiant Google, pour qu’un document n’ait qu’une clé de vote à la fois.',
						'L’empreinte publique de votre clé de vote. La clé elle-même est créée et gardée sur votre appareil : nous ne la recevons jamais.'
					],
					never: [
						'Votre nom, votre prénom, votre date de naissance, votre photo, le numéro de votre document : ils ne quittent pas votre téléphone, où l’app calcule une preuve mathématique que nous vérifions nous-mêmes.',
						'Votre compte : la clé de vote n’est rattachée à aucun compte, et la demande part sans votre connexion.'
					],
					note: 'La demande passe par le relais de ZKPassport, chiffrée de bout en bout.',
					link: ['/verification', 'Comment marche la vérification']
				},
				{
					title: 'Voter',
					get: [
						'Un bulletin anonyme : vos choix (ou votre vote blanc), avec une preuve mathématique qu’il vient d’une des clés de vote admises, sans dire laquelle. Un même votant a un même pseudonyme chiffré par période de 30 jours, pour qu’un nouveau vote remplace l’ancien.',
						'Les bulletins sont publiés par lots, mélangés, dans un journal public que chacun peut télécharger et recompter. Vous y retrouvez le vôtre, depuis vos appareils.'
					],
					never: [
						'Qui a voté quoi : ni nous, ni personne, ne peut relier un bulletin à une clé de vote, à un document ou à un compte. Le bulletin ne porte ni l’un ni l’autre.',
						'Votre clé de vote : sans elle, personne, nous compris, ne peut changer votre vote.'
					],
					note: 'Limites, en clair : l’adresse IP d’un bulletin et l’heure à laquelle il arrive passent par nos serveurs (nous ne les gardons pas, mais un hébergeur contraint pourrait les voir) ; l’option de connexion par Tor est prévue. Et qui a votre clé, ou vos 12 mots, vote à votre place.'
				},
				{
					title: 'Garder sa clé sur plusieurs appareils',
					get: [
						'Avec une clé d’accès (Face ID, empreinte) : une copie de votre clé de vote, chiffrée sur votre appareil avec une clé que seule votre clé d’accès donne. Nous ne pouvons pas l’ouvrir.',
						'En reliant un appareil par QR code : un message chiffré d’un appareil à l’autre, qui transite par nous et s’efface dès qu’il est lu, dans les 10 minutes.'
					],
					never: [
						'Votre clé de vote en clair, ni vos 12 mots de secours : ils ne quittent jamais vos appareils.',
						'Votre visage ou votre empreinte : Face ID reste dans votre téléphone.'
					]
				},
				{
					title: 'Les +1 et les signalements',
					get: [
						'Vos +1, pour classer le fil, et vos signalements, rattachés à votre compte.',
						'Un signalement est relu par une IA avec les sources. S’il est fondé, l’élément est retiré et la correction publiée.'
					],
					never: [
						'La correction publiée montre la raison de l’IA, jamais votre texte ni votre compte.',
						'L’IA reçoit le texte du signalement, jamais qui l’a écrit.'
					],
					note: 'L’IA tourne sur notre propre machine ; quand elle est saturée, un fournisseur d’IA externe peut relire le signalement.'
				},
				{
					title: 'Votre session',
					get: ['Un jeton de connexion, gardé dans votre navigateur (pas un cookie), valable 30 jours. Se déconnecter l’efface.'],
					never: ['Votre mot de passe : il n’y en a pas.']
				}
			],
			eraseTitle: 'Tout effacer',
			erase: [
				'Sur votre profil, « Supprimer mon compte » efface d’un coup le compte, ses sessions et ses +1. Vos signalements restent, détachés de vous : ils ont servi à corriger le site.',
				'Votre vote n’est rattaché à aucun compte : retirez-le sur la page du vote, avec votre clé. Un bulletin retiré ne compte plus ; un bulletin non renouvelé expire après 30 jours.',
				'« Retirer la clé de cet appareil », sur votre profil, l’efface de ce navigateur.'
			],
			promiseTitle: 'Ce que nous ne ferons jamais',
			promises: ['Vendre, louer ou partager des données.', 'Afficher de la publicité.', 'Vous envoyer des e-mails : nous n’avons pas le vôtre.', 'Croiser vos données avec celles d’autres sites.'],
			hosting: 'Le site est hébergé chez the host, qui peut journaliser les connexions comme tout hébergeur.',
			back: 'À propos'
		},
		en: {
			kicker: 'Privacy',
			title: 'We don’t know who you are.',
			lead: 'On purpose: a site that judges politicians must know nothing about its readers or its voters. Here, point by point, is what we receive, what we keep, and what we never see.',
			pills: ['No ads', 'No trackers', 'No e-mail', 'No IP address kept'],
			getLabel: 'What we receive',
			neverLabel: 'Never',
			parts: [
				{
					title: 'Reading the site',
					get: [
						'The pages requested, like any site. Our logs note the page, the time and the answer, not your IP address.',
						'Your IP address serves for one minute, in memory, to slow down abuse (too many requests), then it is gone.'
					],
					never: ['No tracking cookie, no audience measurement, no ads.', 'No script from another site: even the fonts are served by us.'],
					note: 'The politicians’ photos come from their public sources: your browser loads them there.'
				},
				{
					title: 'Signing in with Google',
					get: [
						'An identifier of your Google account: a string of digits, without your name or e-mail.',
						'We don’t even keep it as is. We put it through a one-way “mill” with a secret key only we have: out comes a fingerprint. When you come back, the same mill gives the same fingerprint, and we recognize you. But from the fingerprint there is no way back to the identifier: if our database ever leaked, nobody could tie an account of the site to a Google account.',
						'The pseudonym you choose, which only you see.'
					],
					never: ['Your e-mail, your name, your photo, your contacts.', 'We ask Google only for the “openid” right, the smallest there is: Google’s screen shows it.'],
					note: 'Google knows you sign in to tartuffe.me, as with any “Continue with Google” button.'
				},
				{
					title: 'Getting one’s voting key',
					get: [
						'“Adult: yes” and “French nationality: yes”, proved by the ZKPassport app.',
						'An identifier of your document unique to this site, through the same “mill” as the Google identifier, so one document holds one voting key at a time.',
						'The public fingerprint of your voting key. The key itself is made and kept on your device: we never receive it.'
					],
					never: [
						'Your name, your first name, your birth date, your photo, your document number: they never leave your phone, where the app computes a mathematical proof we check ourselves.',
						'Your account: the voting key is tied to no account, and the request leaves without your sign-in.'
					],
					note: 'The request goes through ZKPassport’s relay, end-to-end encrypted.',
					link: ['/verification', 'How the verification works']
				},
				{
					title: 'Voting',
					get: [
						'An anonymous ballot: your choices (or your blank vote), with a mathematical proof that it comes from one of the admitted voting keys, without saying which. One voter has one encrypted pseudonym per 30-day period, so a new vote replaces the old one.',
						'Ballots are published in shuffled batches, in a public log anyone can download and recount. You find yours there, from your devices.'
					],
					never: [
						'Who voted what: neither we nor anyone can tie a ballot to a voting key, a document or an account. The ballot carries none of them.',
						'Your voting key: without it, nobody, us included, can change your vote.'
					],
					note: 'Limits, plainly: a ballot’s IP address and the time it arrives pass through our servers (we don’t keep them, but a compelled host could see them); a Tor option is planned. And whoever has your key, or your 12 words, votes in your place.'
				},
				{
					title: 'Keeping one’s key on several devices',
					get: [
						'With a passkey (Face ID, fingerprint): a copy of your voting key, encrypted on your device with a key only your passkey gives. We cannot open it.',
						'Linking a device by QR code: an encrypted message from one device to the other, which passes through us and is erased once read, within 10 minutes.'
					],
					never: ['Your voting key in clear, or your 12 recovery words: they never leave your devices.', 'Your face or fingerprint: Face ID stays in your phone.']
				},
				{
					title: '+1 and reports',
					get: ['Your +1, to rank the feed, and your reports, tied to your account.', 'A report is reread by an AI with the sources. When it holds, the item is removed and the correction published.'],
					never: ['The published correction shows the AI’s reason, never your words or your account.', 'The AI gets the report’s text, never who wrote it.'],
					note: 'The AI runs on our own machine; when it is overloaded, an outside AI provider may reread the report.'
				},
				{
					title: 'Your session',
					get: ['A sign-in token, kept in your browser (not a cookie), valid 30 days. Signing out erases it.'],
					never: ['Your password: there is none.']
				}
			],
			eraseTitle: 'Erase everything',
			erase: [
				'On your profile, “Delete my account” erases at once the account, its sessions and its +1. Your reports stay, detached from you: they served to correct the site.',
				'Your vote is tied to no account: take it back on the vote page, with your key. A withdrawn ballot no longer counts; one not renewed expires after 30 days.',
				'“Remove the key from this device”, on your profile, erases it from this browser.'
			],
			promiseTitle: 'What we will never do',
			promises: ['Sell, rent or share data.', 'Show ads.', 'Send you e-mails: we don’t have yours.', 'Match your data with other sites’.'],
			hosting: 'The site is hosted by the host, which may log connections like any host.',
			back: 'About'
		}
	};
	let c = $derived(content[currentLang]);
</script>

<svelte:head>
	<title>{c.kicker} — tartuffe.me</title>
</svelte:head>

<article class="privacy">
	<header class="hero">
		<span class="kicker">{c.kicker}</span>
		<h1>{c.title}</h1>
		<p class="lead">{c.lead}</p>
		<ul class="pills">
			{#each c.pills as p}<li>{p}</li>{/each}
		</ul>
	</header>

	{#each c.parts as part (part.title)}
		<section class="part">
			<h2>{part.title}</h2>
			<div class="cols">
				<div class="col col-get">
					<h3>{c.getLabel}</h3>
					<ul>
						{#each part.get as line}<li>{line}</li>{/each}
					</ul>
				</div>
				<div class="col col-never">
					<h3>{c.neverLabel}</h3>
					<ul>
						{#each part.never as line}<li>{line}</li>{/each}
					</ul>
				</div>
			</div>
			{#if part.note || part.link}
				<p class="note">
					{part.note ?? ''}
					{#if part.link}<a href={part.link[0]}>{part.link[1]} →</a>{/if}
				</p>
			{/if}
		</section>
	{/each}

	<section class="part">
		<h2>{c.eraseTitle}</h2>
		<ul class="plain">
			{#each c.erase as line}<li>{line}</li>{/each}
		</ul>
	</section>

	<section class="part">
		<h2>{c.promiseTitle}</h2>
		<ul class="promises">
			{#each c.promises as line}<li>{line}</li>{/each}
		</ul>
		<p class="note">{c.hosting}</p>
	</section>

	<p class="back"><a href="/about">← {c.back}</a></p>
</article>

<style>
	.privacy {
		max-width: 46rem;
		margin: 0 auto;
	}
	.hero {
		text-align: center;
		margin: 1rem 0 3.5rem;
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
		margin: 0.4rem auto 1rem;
		max-width: 36rem;
		text-wrap: balance;
	}
	.lead {
		font-size: 1.05rem;
		color: var(--text-secondary);
		line-height: 1.6;
		max-width: 38rem;
		margin: 0 auto 1.25rem;
		text-wrap: balance;
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
	.part {
		margin-bottom: 2.75rem;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: 1.6rem;
		font-weight: 600;
		margin: 0 0 0.9rem;
	}
	.cols {
		display: grid;
		grid-template-columns: repeat(2, minmax(0, 1fr));
		gap: 0.9rem;
	}
	.col {
		padding: 1rem 1.1rem;
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		border: 1px solid var(--border-card);
	}
	.col-get {
		border-top: 2px solid var(--green);
	}
	.col-never {
		border-top: 2px solid var(--red);
	}
	h3 {
		margin: 0 0 0.5rem;
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--text-muted);
	}
	ul {
		margin: 0;
		padding-left: 1.1rem;
	}
	li {
		line-height: 1.55;
		margin-bottom: 0.4rem;
		color: var(--text-primary);
	}
	.note {
		margin: 0.7rem 0 0;
		font-size: 0.88rem;
		color: var(--text-secondary);
		line-height: 1.55;
	}
	.plain li,
	.promises li {
		color: var(--text-secondary);
	}
	.promises {
		list-style: none;
		padding: 0;
	}
	.promises li::before {
		content: '✕';
		margin-right: 0.6rem;
		color: var(--red);
		font-weight: 700;
	}
	.back {
		margin: 3rem 0 1rem;
		text-align: center;
	}
	@media (max-width: 640px) {
		.cols {
			grid-template-columns: minmax(0, 1fr);
		}
		.hero {
			margin-bottom: 2.5rem;
		}
	}
</style>
