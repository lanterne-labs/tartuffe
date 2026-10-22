<script lang="ts">
	// Privacy: what the site receives, keeps and never sees, feature by
	// feature (reading, the Google account, the identity check, the votes, the
	// +1 and the reports, the session), and how to erase everything. Every line
	// must stay true to the code: backend/accounts, backend/polls, the access
	// log of backend/api, the fonts served by the site.
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
					title: 'Vérifier son identité, pour voter',
					get: [
						'« Majeur : oui » et « nationalité française : oui ».',
						'Un identifiant de votre document propre à ce site, pour qu’un document ne vote qu’une fois. Il ne sert nulle part ailleurs et ne permet pas de remonter à vous.'
					],
					never: [
						'Votre nom, votre prénom, votre date de naissance, votre photo, le numéro de votre document.',
						'Ils ne quittent pas votre téléphone : l’app ZKPassport y calcule une preuve mathématique, que nous vérifions nous-mêmes.'
					],
					note: 'La demande passe par le relais de ZKPassport, chiffrée de bout en bout. Si votre document est déjà lié à un autre compte (un ancien compte Google, par exemple), nous vous demandons avant de le transférer, avec ses votes.',
					link: ['/verification', 'Comment marche la vérification']
				},
				{
					title: 'Voter',
					get: [
						'Vos choix (vos candidats, dans votre ordre), rattachés à votre compte, seulement après votre accord : un vote est une opinion politique, une donnée sensible.'
					],
					never: [
						'Votre vote n’est jamais montré seul : seuls les totaux s’affichent, à partir d’un nombre minimum de votants, et jamais pendant la période d’interdiction légale.',
						'Aucune page ni aucune requête ne donne le vote, le pseudo ou les +1 d’une autre personne.'
					],
					note: 'Vous changez ou retirez vos votes à tout moment jusqu’à la clôture.'
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
			eraseTitle: 'Tout effacer, d’un clic',
			erase: [
				'Sur votre profil, « Supprimer mon compte » efface d’un coup le compte, ses sessions, son identité vérifiée, ses votes et ses +1.',
				'Vos signalements restent, détachés de vous : ils ont servi à corriger le site.',
				'Vous reconnecter ensuite ouvre un compte vierge.'
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
					title: 'Verifying one’s identity, to vote',
					get: [
						'“Adult: yes” and “French nationality: yes”.',
						'An identifier of your document unique to this site, so one document votes once. It serves nowhere else and does not lead back to you.'
					],
					never: [
						'Your name, your first name, your birth date, your photo, your document number.',
						'They never leave your phone: the ZKPassport app computes a mathematical proof there, which we check ourselves.'
					],
					note: 'The request goes through ZKPassport’s relay, end-to-end encrypted. If your document is already tied to another account (an old Google account, say), we ask you before moving it, with its votes.',
					link: ['/verification', 'How the verification works']
				},
				{
					title: 'Voting',
					get: ['Your choices (your candidates, in your order), tied to your account, only after you agree: a vote is a political opinion, sensitive data.'],
					never: [
						'Your vote is never shown on its own: only the totals are, from a minimum number of voters, and never during the legal silence period.',
						'No page and no request gives another person’s vote, pseudonym or +1.'
					],
					note: 'You change or take back your votes at any time until voting closes.'
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
			eraseTitle: 'Erase everything, in one click',
			erase: [
				'On your profile, “Delete my account” erases at once the account, its sessions, its verified identity, its votes and its +1.',
				'Your reports stay, detached from you: they served to correct the site.',
				'Signing in again then opens a blank account.'
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
