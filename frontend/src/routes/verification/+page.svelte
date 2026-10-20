<script lang="ts">
	// How the identity check of the votes works (ZKPassport), for every reader:
	// what happens, what the site receives, what it never sees, and the limits.
	import { lang } from '$lib/i18n';
	import { signInOpen } from '$lib/auth';

	let currentLang = $state<'fr' | 'en'>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));

	const content = {
		fr: {
			title: 'Vérifier sans s’identifier',
			lead: 'Pour que le pré-sondage compte une voix par personne, chaque votant prouve qu’il est un électeur français majeur. Sans compte, sans e-mail, sans donner son nom : avec une preuve à divulgation nulle de connaissance.',
			cta: 'Vérifier mon document',
			stepsTitle: 'Comment ça se passe',
			steps: [
				['Le site prépare une demande', 'Il demande seulement : « majeur ? », « nationalité française ? », et y attache un code à usage unique pour que la preuve ne serve qu’une fois.'],
				['Votre téléphone lit la puce', 'Avec l’app ZKPassport, vous scannez le QR code puis posez votre passeport ou votre carte d’identité biométrique contre le téléphone (NFC). La puce contient vos données signées par l’État qui a émis le document.'],
				['La preuve est calculée sur le téléphone', 'Vos données ne quittent pas le téléphone. Il calcule une preuve mathématique qui dit : « ce document est authentique, signé par l’État, non expiré ; son titulaire a plus de 18 ans et est français ».'],
				['Nos serveurs vérifient la preuve', 'Nous vérifions la preuve nous-mêmes, avec le vérificateur open source de ZKPassport. Si elle est valide, vous êtes connecté et pouvez voter.']
			],
			getTitle: 'Ce que nous recevons',
			get: [
				'« Majeur : oui » et « Nationalité française : oui ». Rien de plus précis.',
				'Un identifiant anonyme, propre à tartuffe.me (voir plus bas), qui empêche de voter deux fois.',
				'Votre classement, si vous votez. Il n’est jamais publié seul : seuls les totaux le sont.'
			],
			neverTitle: 'Ce que nous ne voyons jamais',
			never: [
				'Votre nom, votre prénom, votre photo',
				'Votre date de naissance exacte',
				'Le numéro de votre document',
				'Votre adresse, votre e-mail, votre numéro de téléphone'
			],
			zkTitle: 'Une preuve « à divulgation nulle de connaissance » ?',
			zk: [
				'C’est une technique de cryptographie qui permet de prouver qu’une affirmation est vraie sans révéler les informations qui la rendent vraie.',
				'Une image : vous voulez prouver que vous avez plus de 18 ans à un videur sans lui montrer votre carte. Un notaire regarde votre carte en secret et vous remet un certificat « majeur », impossible à falsifier. Ici, le notaire est un calcul mathématique fait par votre téléphone, et n’importe qui peut vérifier le certificat sans faire confiance au téléphone.',
				'La preuve s’appuie sur la signature numérique de l’État présente dans la puce : un faux document ne peut pas produire une preuve valide.'
			],
			idTitle: 'L’identifiant anonyme',
			id: [
				'Il est calculé à partir de votre document, du nom du site (tartuffe.me) et d’un nom d’usage fixe. Le même document donne toujours le même identifiant ici : c’est ce qui garantit une voix par document.',
				'Il est différent sur chaque site : deux sites ne peuvent pas recouper leurs utilisateurs avec. Et il ne permet pas de remonter à votre nom.'
			],
			limitsTitle: 'Les limites, honnêtement',
			limits: [
				'Il faut un smartphone avec NFC et un document à puce (passeport biométrique ou carte d’identité au format carte bancaire). Sans eux, vous ne pouvez pas encore voter.',
				'L’identifiant est lié au document, pas à la personne : quelqu’un qui a à la fois un passeport et une carte d’identité valides pourrait voter deux fois. Un document expiré est refusé.',
				'Renouveler son document donne un nouvel identifiant.',
				'Quelqu’un qui posséderait une copie complète des données de la puce de votre document pourrait recalculer votre identifiant. Il ne verrait qu’un classement, jamais publié seul.',
				'La demande transite entre le site et votre téléphone par le relais de ZKPassport, chiffrée de bout en bout. La vérification, elle, se fait sur nos serveurs.',
				'ZKPassport est open source et a fait l’objet d’audits internes, mais pas encore d’audit externe.'
			],
			techTitle: 'Pour les curieux',
			tech: [
				['Code de ZKPassport (licence Apache-2.0)', 'https://github.com/zkpassport'],
				['Site de ZKPassport', 'https://zkpassport.id']
			],
			techNote: 'La vérification n’utilise ni compte ni clé ZKPassport : notre serveur exécute le vérificateur inclus dans leur kit open source et lit seulement, sur la blockchain Ethereum, le registre public des certificats des États émetteurs.',
			back: 'Retour au pré-sondage'
		},
		en: {
			title: 'Verified without being identified',
			lead: 'So that the pre-poll counts one vote per person, each voter proves they are an adult French voter. No account, no email, no name: with a zero-knowledge proof.',
			cta: 'Verify my document',
			stepsTitle: 'What happens',
			steps: [
				['The site prepares a request', 'It only asks: “adult?”, “French national?”, and attaches a one-time code so the proof can serve only once.'],
				['Your phone reads the chip', 'With the ZKPassport app, you scan the QR code and hold your passport or biometric ID card against the phone (NFC). The chip holds your data, signed by the State that issued the document.'],
				['The proof is computed on the phone', 'Your data never leaves the phone. It computes a mathematical proof saying: “this document is genuine, signed by the State, not expired; its holder is over 18 and French”.'],
				['Our servers check the proof', 'We check the proof ourselves, with ZKPassport’s open-source verifier. If it is valid, you are signed in and can vote.']
			],
			getTitle: 'What we receive',
			get: [
				'“Adult: yes” and “French national: yes”. Nothing more precise.',
				'An anonymous identifier, specific to tartuffe.me (see below), that prevents voting twice.',
				'Your ranking, if you vote. It is never published on its own: only the totals are.'
			],
			neverTitle: 'What we never see',
			never: ['Your name, your photo', 'Your exact date of birth', 'Your document number', 'Your address, email or phone number'],
			zkTitle: 'A “zero-knowledge” proof?',
			zk: [
				'It is a cryptographic technique that proves a statement is true without revealing the information that makes it true.',
				'An analogy: you want to prove to a bouncer you are over 18 without showing your card. A notary looks at your card in private and hands you an unforgeable “adult” certificate. Here the notary is a computation done by your phone, and anyone can check the certificate without trusting the phone.',
				'The proof relies on the State’s digital signature in the chip: a fake document cannot produce a valid proof.'
			],
			idTitle: 'The anonymous identifier',
			id: [
				'It is computed from your document, the site’s name (tartuffe.me) and a fixed use-case name. The same document always gives the same identifier here: that is what guarantees one vote per document.',
				'It differs on every site, so two sites cannot cross-match their users with it. And it does not lead back to your name.'
			],
			limitsTitle: 'The limits, honestly',
			limits: [
				'You need a smartphone with NFC and a chip document (biometric passport or credit-card-size ID card). Without them, you cannot vote yet.',
				'The identifier belongs to the document, not the person: someone holding both a valid passport and a valid ID card could vote twice. An expired document is refused.',
				'Renewing your document gives a new identifier.',
				'Someone holding a full copy of your document’s chip data could recompute your identifier. They would only see a ranking, never published on its own.',
				'The request travels between the site and your phone through ZKPassport’s relay, end-to-end encrypted. The verification itself runs on our servers.',
				'ZKPassport is open source and has had internal audits, but no external audit yet.'
			],
			techTitle: 'For the curious',
			tech: [
				['ZKPassport source code (Apache-2.0 license)', 'https://github.com/zkpassport'],
				['ZKPassport website', 'https://zkpassport.id']
			],
			techNote: 'The verification uses no ZKPassport account or key: our server runs the verifier bundled in their open-source kit and only reads, on the Ethereum blockchain, the public registry of the issuing States’ certificates.',
			back: 'Back to the pre-poll'
		}
	};

	let c = $derived(content[currentLang]);
</script>

<svelte:head>
	<title>{c.title} — tartuffe.me</title>
	<meta name="description" content={c.lead} />
</svelte:head>

<article class="page">
	<header class="hero">
		<span class="kicker">ZKPassport</span>
		<h1>{c.title}</h1>
		<p class="lead">{c.lead}</p>
		<button class="cta" onclick={() => signInOpen.set(true)}>{c.cta}</button>
	</header>

	<section>
		<h2>{c.stepsTitle}</h2>
		<ol class="steps">
			{#each c.steps as [title, body], i}
				<li>
					<span class="n">{i + 1}</span>
					<div>
						<h3>{title}</h3>
						<p>{body}</p>
					</div>
				</li>
			{/each}
		</ol>
	</section>

	<section class="split">
		<div class="box box--get">
			<h2>{c.getTitle}</h2>
			<ul>
				{#each c.get as line}<li>{line}</li>{/each}
			</ul>
		</div>
		<div class="box box--never">
			<h2>{c.neverTitle}</h2>
			<ul>
				{#each c.never as line}<li>{line}</li>{/each}
			</ul>
		</div>
	</section>

	<section>
		<h2>{c.zkTitle}</h2>
		{#each c.zk as p}<p>{p}</p>{/each}
	</section>

	<section>
		<h2>{c.idTitle}</h2>
		{#each c.id as p}<p>{p}</p>{/each}
	</section>

	<section>
		<h2>{c.limitsTitle}</h2>
		<ul class="limits">
			{#each c.limits as line}<li>{line}</li>{/each}
		</ul>
	</section>

	<section>
		<h2>{c.techTitle}</h2>
		<p>{c.techNote}</p>
		<ul>
			{#each c.tech as [label, href]}<li><a {href} target="_blank" rel="noopener">{label}</a></li>{/each}
		</ul>
	</section>

	<p class="back"><a href="/election/presidentielle-2027">← {c.back}</a></p>
</article>

<style>
	.page {
		max-width: 46rem;
		margin: 0 auto;
	}
	.hero {
		text-align: center;
		margin: 1rem 0 2.5rem;
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
		font-size: clamp(2rem, 6vw, 3rem);
		font-weight: 600;
		line-height: 1.1;
		margin: 0.25rem 0 0.75rem;
	}
	.lead {
		font-size: 1.05rem;
		color: var(--text-secondary);
		line-height: 1.55;
		margin: 0 0 1.25rem;
	}
	.cta {
		padding: 0.75rem 1.5rem;
		border: none;
		border-radius: var(--radius-full);
		background: var(--accent);
		color: white;
		font-weight: 600;
		font-size: 0.95rem;
		cursor: pointer;
	}
	.cta:hover {
		background: var(--accent-hover);
	}
	section {
		margin-bottom: 2.25rem;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: 1.5rem;
		font-weight: 600;
		margin: 0 0 0.75rem;
	}
	h3 {
		font-size: 1rem;
		margin: 0 0 0.25rem;
	}
	p,
	li {
		color: var(--text-secondary);
		line-height: 1.6;
		font-size: 0.95rem;
	}
	p {
		margin: 0 0 0.75rem;
	}
	ul {
		margin: 0;
		padding-left: 1.2rem;
		display: flex;
		flex-direction: column;
		gap: 0.4rem;
	}
	.steps {
		list-style: none;
		margin: 0;
		padding: 0;
		display: flex;
		flex-direction: column;
		gap: 1rem;
	}
	.steps li {
		display: flex;
		gap: 1rem;
		align-items: flex-start;
	}
	.steps p {
		margin: 0;
	}
	.n {
		width: 2rem;
		height: 2rem;
		flex-shrink: 0;
		display: grid;
		place-items: center;
		border-radius: 50%;
		background: var(--accent);
		color: white;
		font-weight: 700;
	}
	.split {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1rem;
	}
	.box {
		padding: 1.1rem 1.25rem;
		border-radius: var(--radius-lg);
		background: var(--bg-card);
		border: 1px solid var(--border-card);
	}
	.box h2 {
		font-size: 1.2rem;
	}
	.box--get {
		border-color: var(--green);
	}
	.box--never {
		border-color: var(--red);
	}
	.limits li::marker {
		color: var(--amber);
	}
	.back {
		text-align: center;
		margin: 2rem 0 1rem;
	}
	@media (max-width: 640px) {
		.split {
			grid-template-columns: 1fr;
		}
	}
</style>
