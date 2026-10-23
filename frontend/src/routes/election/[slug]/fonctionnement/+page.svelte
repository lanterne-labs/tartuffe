<script lang="ts">
	// How the event's vote works, for every reader: the voting key (the
	// document check, ZKPassport), the anonymous ballot and its way out
	// through the Nostr relays, the two counts, the blank vote, the public log
	// and its checks, the devices, what the site receives and never sees, and
	// the limits. Every line must stay true to the code: backend/polls (and
	// its semaphore package, votelog.go, checkpoint.go, nostr.go, stamps.go,
	// audit.go), backend/accounts, lib/voter.ts, lib/anonballot.ts,
	// lib/nostr.ts, lib/votelog.ts.
	import { page } from '$app/stores';
	import { lang, type Lang } from '$lib/i18n';
	import { eventPaths } from '$lib/event';
	import SequenceDiagram, { type Actor, type Step } from '$lib/components/SequenceDiagram.svelte';
	import ZKProofAnimation from '$lib/components/ZKProofAnimation.svelte';

	let currentLang = $state<Lang>('fr');
	$effect(() => lang.subscribe((v) => (currentLang = v)));
	let paths = $derived(eventPaths($page.params.slug ?? ''));

	type Diagram = { actors: Actor[]; steps: Step[]; caption: string };
	type Part = { id: string; title: string; body: string[]; diagram?: Diagram; list?: string[]; more?: [string, string[]][] };

	const content: Record<
		Lang,
		{
			kicker: string;
			title: string;
			lead: string;
			toc: string;
			parts: Part[];
			getTitle: string;
			get: string[];
			neverTitle: string;
			never: string[];
			limitsTitle: string;
			limitsLead: string;
			limits: string[];
			eraseTitle: string;
			erase: string[];
			techTitle: string;
			tech: string[];
			links: [string, string][];
			cta: string;
		}
	> = {
		fr: {
			kicker: 'Comment ça marche',
			title: 'Un vote que personne ne peut relier à vous. Pas même nous.',
			lead: 'Votre document prouve que vous êtes un électeur français majeur. Votre vote, lui, ne porte ni votre document, ni votre compte, ni votre adresse. Et chacun peut recompter. Voici comment, étape par étape.',
			toc: 'Sommaire',
			parts: [
				{
					id: 'cle',
					title: '1. Obtenir sa clé de vote',
					body: [
						'Pourquoi un document d’identité ? Pour qu’une personne ne vote qu’une fois : sans cela, n’importe qui pourrait créer cent comptes et fausser les résultats. Le passeport ou la carte d’identité biométrique prouve seulement que vous êtes un électeur français majeur. Ni nom, ni photo, ni e-mail.',
						'Votre navigateur crée d’abord une clé de vote secrète, qui ne le quitte jamais, et son empreinte publique. Puis l’app ZKPassport lit la puce de votre document (NFC) et calcule, sur votre téléphone, une preuve mathématique : « ce document est authentique, signé par l’État, non expiré ; son titulaire a plus de 18 ans et est français ». Cette preuve est attachée à l’empreinte de votre clé, pour ne servir qu’à elle, une seule fois.',
						'Nous vérifions la preuve nous-mêmes. Nous gardons un identifiant de votre document passé dans une « moulinette » à sens unique (une empreinte HMAC, avec une clé secrète que nous seuls avons), pour qu’un document n’ait qu’une clé de vote à la fois, et l’empreinte publique de votre clé, qui rejoint la liste des votants. Rien ne relie l’un à votre bulletin.'
					],
					diagram: {
						actors: [{ label: 'Votre navigateur' }, { label: 'App ZKPassport', sub: 'votre téléphone' }, { label: 'tartuffe.me' }, { label: 'Ethereum', sub: 'registre public' }],
						steps: [
							{ at: 0, note: 'Crée la clé de vote secrète s et son empreinte publique C. s reste sur l’appareil.', span: 2 },
							{ from: 0, to: 2, label: 'demande un défi, sans compte ni session' },
							{ from: 2, to: 0, label: 'défi à usage unique', dashed: true },
							{ from: 0, to: 1, label: 'QR code : « majeur ? français ? » lié à H(défi, C)' },
							{ at: 1, note: 'Lit la puce (NFC), calcule la preuve. Les données restent sur le téléphone.', span: 2 },
							{ from: 1, to: 0, label: 'preuve : oui, oui. Pas de nom, pas de numéro', dashed: true },
							{ from: 0, to: 2, label: 'preuve + C' },
							{ from: 2, to: 3, label: 'lit les certificats des États (lecture seule)' },
							{ at: 2, note: 'Vérifie la preuve. Garde HMAC(document) et C. C rejoint la liste des votants.', span: 2 },
							{ from: 2, to: 0, label: 'inscrit : vous pouvez voter', dashed: true }
						],
						caption: 'Le document prouve un droit de vote ; la clé, gardée par vous, votera. Rien ne relie les deux au bulletin.'
					},
					more: [
						[
							'Une preuve « à divulgation nulle de connaissance » ?',
							[
								'C’est une technique de cryptographie qui permet de prouver qu’une affirmation est vraie sans révéler les informations qui la rendent vraie.',
								'Une image : vous voulez prouver à un videur que vous avez plus de 18 ans sans lui montrer votre carte. Un notaire regarde votre carte en secret et vous remet un certificat « majeur », impossible à falsifier. Ici, le notaire est un calcul fait par votre téléphone, et n’importe qui peut vérifier le certificat sans faire confiance au téléphone.',
								'La preuve s’appuie sur la signature numérique de l’État présente dans la puce : un faux document ne peut pas produire une preuve valide.'
							]
						],
						[
							'L’identifiant du document',
							[
								'Il est calculé à partir de votre document et du nom du site. Le même document donne toujours le même identifiant ici : c’est ce qui garantit une voix par document. Il diffère sur chaque site, et ne permet pas de remonter à votre nom.',
								'Nous ne le gardons pas tel quel : il passe dans notre « moulinette » à sens unique. Si notre base fuitait, elle ne donnerait pas la liste des documents.'
							]
						]
					]
				},
				{
					id: 'vote',
					title: '2. Voter',
					body: [
						'Vous classez jusqu’à 3 candidats en les touchant dans l’ordre : le premier est votre favori. Ou vous votez blanc. Vous pouvez changer d’avis à tout moment jusqu’à la clôture : le dernier vote compte.',
						'Votre navigateur télécharge toute la liste des votants (toutes les empreintes, pas seulement la vôtre : la demande ne dit pas qui vous êtes) et calcule une preuve Semaphore : « ce bulletin vient d’une des clés de la liste », sans dire laquelle. La preuve donne aussi un pseudonyme de période, toujours le même pour vous sur 30 jours et impossible à relier à votre clé : c’est lui qui fait qu’un nouveau vote remplace l’ancien.',
						'Le bulletin ne part pas chez nous : il est publié sur des relais Nostr publics, tenus par d’autres (nos.lol, relay.primal.net, offchain.pub, nostr.mom, relay.snort.social). Chaque envoi est signé par une clé jetable et daté à l’heure près. Notre serveur lit les relais une fois par heure : il ne voit jamais l’adresse IP d’un bulletin, ni à quelle minute il est parti.',
						'Les bulletins sont publiés par lots, mélangés, chaque heure (au moins 3 à la fois, ou après 6 h d’attente au plus), dans le journal public. Si aucun relais ne répond, votre navigateur envoie le bulletin directement à notre serveur, qui voit alors l’adresse d’où il vient.'
					],
					diagram: {
						actors: [{ label: 'Votre navigateur' }, { label: 'Relais Nostr', sub: 'publics, tenus par d’autres' }, { label: 'tartuffe.me' }, { label: 'Journal public' }],
						steps: [
							{ from: 0, to: 2, label: 'télécharge toute la liste des votants' },
							{ at: 0, note: 'Prouve « une des clés de la liste », lie la preuve au bulletin ①②③ et donne le pseudonyme de période.', span: 2 },
							{ from: 0, to: 1, label: 'bulletin + preuve, signé d’une clé jetable, daté à l’heure' },
							{ from: 1, to: 0, label: 'OK (plusieurs relais)', dashed: true },
							{ from: 2, to: 1, label: 'lit les nouveaux bulletins, une fois par heure' },
							{ at: 2, note: 'Vérifie chaque preuve. Un nouveau vote remplace l’ancien du même pseudonyme.', span: 2 },
							{ from: 2, to: 3, label: 'publie le lot mélangé et les nouveaux totaux' }
						],
						caption: 'Notre serveur ne reçoit pas le bulletin de vous : il le lit sur les relais, avec tous les autres.'
					},
					more: [
						[
							'Pourquoi des périodes de 30 jours ?',
							[
								'Un bulletin compte pour une période de 30 jours. Deux séries de périodes tournent, la seconde décalée de 15 jours, et votre navigateur vote dans les deux : les résultats lisent la période commencée la première. Il renouvelle votre bulletin tout seul quand une nouvelle période s’ouvre, si vous revenez sur le site.',
								'C’est ce qui permet de remplacer une clé perdue sans jamais compter deux voix pour un document : une nouvelle clé vote à partir de la période suivante.'
							]
						]
					]
				},
				{
					id: 'comptage',
					title: '3. Deux façons de compter les mêmes votes',
					body: [
						'« Avec le classement » tient compte de vos 2ᵉ et 3ᵉ choix. On compare les candidats deux à deux : A passe devant B si plus de votants classent A au-dessus de B (un candidat classé passe devant un candidat non classé). Le gagnant est celui qui bat chacun des autres en face-à-face : c’est le gagnant de Condorcet. Quand aucun ne bat tous les autres, la méthode de Schulze donne quand même un ordre.',
						'« Au scrutin majoritaire » ne garde que les premiers choix, comme le premier tour d’une élection. Quand les deux gagnants diffèrent, la page le dit : le scrutin majoritaire a laissé de côté une préférence de la majorité.',
						'Les résultats sont masqués tant qu’il y a trop peu de votants, et la veille et le jour d’un scrutin, comme la loi l’exige. Ce n’est pas un sondage : les votants ne sont pas représentatifs de la population.'
					]
				},
				{
					id: 'blanc',
					title: '4. Le vote blanc compte',
					body: [
						'Le vote blanc est un bulletin à part entière : « je suis venu, j’ai voté, aucun ne me convainc ». Il exclut tout classement, il compte parmi les votants, et donc dans chaque pourcentage. Il a sa propre ligne dans les résultats.',
						'Retirer son vote, c’est autre chose : le bulletin ne compte plus du tout.'
					]
				},
				{
					id: 'journal',
					title: '5. Vérifier, sans nous faire confiance',
					body: [
						'Chaque inscription, chaque liste de votants, chaque bulletin et chaque total entre dans un journal public, construit comme ceux qui surveillent les certificats du web : un arbre de Merkle. Notre serveur signe régulièrement la « tête » de l’arbre (un point de contrôle) ; un point de contrôle engage tout ce qui le précède.',
						'Votre navigateur garde le dernier point de contrôle qu’il a vu. À chaque visite, il demande la preuve que le nouveau prolonge l’ancien, sans rien effacer ni réécrire, puis la preuve que votre bulletin est dans l’arbre, tel que vous l’avez fait. Sinon, il vous le dit et vous donne la preuve signée de l’anomalie, à publier.',
						'À chaque lot, l’empreinte du point de contrôle est inscrite dans la blockchain Bitcoin, gratuitement, par OpenTimestamps : personne ne peut ensuite prétendre qu’un autre journal existait à cette date. Et n’importe qui peut télécharger tout le journal et tout recompter avec l’outil open source « dm poll verify » : les inscriptions, les listes de votants, chaque preuve et chaque total, en comparant avec les bulletins restés sur les relais Nostr.'
					],
					diagram: {
						actors: [{ label: 'Votre navigateur' }, { label: 'tartuffe.me' }, { label: 'Bitcoin', sub: 'OpenTimestamps' }, { label: 'N’importe qui' }],
						steps: [
							{ at: 1, note: 'Chaque entrée devient une feuille de l’arbre de Merkle du journal.', span: 2 },
							{ from: 1, to: 2, label: 'empreinte du point de contrôle signé, à chaque lot' },
							{ from: 0, to: 1, label: 'demande le point de contrôle signé' },
							{ from: 1, to: 0, label: 'taille, racine, signature + preuve qu’il prolonge le dernier vu', dashed: true },
							{ from: 0, to: 1, label: 'demande la preuve que mon bulletin est dans l’arbre' },
							{ at: 0, note: 'Vérifie : le journal n’a fait que grandir, et il contient mon bulletin tel que je l’ai fait. Sinon : preuve signée à télécharger.', span: 2 },
							{ from: 3, to: 1, label: 'télécharge tout le journal' },
							{ at: 3, note: 'dm poll verify : rejoue inscriptions, listes, preuves et totaux ; compare avec les relais.', span: 2 }
						],
						caption: 'Nous ne pouvons pas changer, effacer ou oublier un bulletin sans que cela se voie.'
					}
				},
				{
					id: 'appareils',
					title: '6. Plusieurs appareils, et une clé perdue',
					body: [
						'Votre clé de vote vit sur votre appareil. Pour la retrouver ailleurs, trois moyens : une clé d’accès (Face ID, empreinte), qui garde chez nous une copie chiffrée sur votre appareil, que seule votre clé d’accès ouvre ; relier un appareil par QR code, comme WhatsApp : un message chiffré d’un appareil à l’autre, effacé dès qu’il est lu, dans les 10 minutes ; ou 12 mots de secours, à noter.',
						'Tout perdu ? Scannez à nouveau votre document : une nouvelle clé est créée, et elle vote à partir de la période de 30 jours suivante. D’ici là, votre ancien vote compte toujours. Ainsi, un document ne pèse jamais deux voix.'
					]
				}
			],
			getTitle: 'Ce que nous recevons',
			get: [
				'« Majeur : oui » et « nationalité française : oui », prouvés par l’app ZKPassport.',
				'L’empreinte HMAC de l’identifiant de votre document, pour une clé de vote par document.',
				'L’empreinte publique de votre clé de vote. La clé elle-même ne quitte jamais vos appareils.',
				'Des bulletins anonymes, lus sur les relais Nostr, avec leur preuve. Ils sont publiés, mélangés, dans le journal public.'
			],
			neverTitle: 'Ce que nous ne voyons jamais',
			never: [
				'Votre nom, votre photo, votre date de naissance, le numéro de votre document.',
				'Qui a voté quoi : un bulletin ne porte ni votre document, ni votre clé, ni votre compte.',
				'L’adresse IP d’un bulletin envoyé par les relais, ni l’heure exacte de son envoi.',
				'Votre clé de vote en clair, vos 12 mots, votre visage ou votre empreinte.'
			],
			limitsTitle: 'Les limites, honnêtement',
			limitsLead: 'Aucun vote en ligne n’est parfait. Voici ce que celui-ci ne garantit pas.',
			limits: [
				'Les relais Nostr voient l’adresse IP de votre navigateur, comme tout site qu’il ouvre ; votre fournisseur d’accès voit que vous vous connectez à eux. Une option Tor n’existe pas encore.',
				'Si aucun relais ne répond, le bulletin part directement chez nous, et notre serveur voit alors son adresse IP (nous ne la gardons pas).',
				'Un votant qui le veut peut prouver son vote à quelqu’un : l’achat de votes n’est pas empêché. Et qui a votre clé, ou vos 12 mots, vote à votre place.',
				'Le code de cette page est servi par nous : un serveur malveillant pourrait servir un autre code. Le code est open source, et le journal est vérifiable par d’autres.',
				'Un seul serveur reçoit et publie les bulletins. Le journal signé et ancré dans Bitcoin rend la triche visible, pas impossible.',
				'Il faut un smartphone avec NFC et un document à puce. L’identifiant est lié au document, pas à la personne : quelqu’un qui a un passeport et une carte d’identité valides pourrait voter deux fois. Renouveler son document donne un nouvel identifiant.',
				'Qui posséderait une copie complète des données de la puce de votre document pourrait recalculer son identifiant : il saurait que ce document a une clé de vote, jamais pour qui elle a voté.',
				'La demande ZKPassport transite par le relais de ZKPassport, chiffrée de bout en bout. ZKPassport est open source et a eu des audits internes, pas encore d’audit externe.'
			],
			eraseTitle: 'Retirer son vote, effacer sa clé',
			erase: [
				'Votre vote n’est rattaché à aucun compte : retirez-le sur la page du vote, avec votre clé. Un bulletin non renouvelé expire après 30 jours.',
				'« Retirer la clé de cet appareil », sur votre profil, l’efface de ce navigateur.'
			],
			techTitle: 'Pour les curieux',
			tech: [
				'Les preuves de vote sont des preuves Semaphore (Groth16), vérifiées par notre serveur en Go, avec les clés de vérification de la cérémonie publique de Semaphore.',
				'Le journal est un arbre de Merkle au sens de la RFC 6962 ; ses points de contrôle sont des « notes signées » Ed25519 (format C2SP), servis sur /api/vote-log/checkpoint avec leurs preuves.',
				'La vérification ZKPassport n’utilise ni compte ni clé ZKPassport : notre serveur exécute le vérificateur de leur kit open source et lit seulement, sur Ethereum, le registre public des certificats des États émetteurs.',
				'Recompter soi-même : dm poll verify --api https://tartuffe.me --relays.'
			],
			links: [
				['Code de ZKPassport (Apache-2.0)', 'https://github.com/zkpassport'],
				['Semaphore', 'https://semaphore.pse.dev'],
				['OpenTimestamps', 'https://opentimestamps.org']
			],
			cta: 'Aller voter'
		},
		en: {
			kicker: 'How it works',
			title: 'A vote nobody can tie to you. Not even us.',
			lead: 'Your document proves you are an adult French voter. Your vote carries neither your document, nor your account, nor your address. And anyone can recount. Here is how, step by step.',
			toc: 'Contents',
			parts: [
				{
					id: 'cle',
					title: '1. Getting one’s voting key',
					body: [
						'Why an ID document? So each person votes only once: without it, anyone could create a hundred accounts and skew the results. The passport or biometric ID card only proves you are an adult French voter. No name, no photo, no e-mail.',
						'Your browser first makes a secret voting key, which never leaves it, and its public fingerprint. Then the ZKPassport app reads your document’s chip (NFC) and computes, on your phone, a mathematical proof: “this document is genuine, signed by the State, not expired; its holder is over 18 and French”. The proof is tied to your key’s fingerprint, to serve it alone, once.',
						'We check the proof ourselves. We keep an identifier of your document put through a one-way “mill” (an HMAC fingerprint, with a secret key only we have), so one document holds one voting key at a time, and your key’s public fingerprint, which joins the list of voters. Nothing ties either to your ballot.'
					],
					diagram: {
						actors: [{ label: 'Your browser' }, { label: 'ZKPassport app', sub: 'your phone' }, { label: 'tartuffe.me' }, { label: 'Ethereum', sub: 'public registry' }],
						steps: [
							{ at: 0, note: 'Makes the secret voting key s and its public fingerprint C. s stays on the device.', span: 2 },
							{ from: 0, to: 2, label: 'asks for a challenge, without account or session' },
							{ from: 2, to: 0, label: 'one-time challenge', dashed: true },
							{ from: 0, to: 1, label: 'QR code: “adult? French?” tied to H(challenge, C)' },
							{ at: 1, note: 'Reads the chip (NFC), computes the proof. The data stays on the phone.', span: 2 },
							{ from: 1, to: 0, label: 'proof: yes, yes. No name, no number', dashed: true },
							{ from: 0, to: 2, label: 'proof + C' },
							{ from: 2, to: 3, label: 'reads the States’ certificates (read only)' },
							{ at: 2, note: 'Checks the proof. Keeps HMAC(document) and C. C joins the list of voters.', span: 2 },
							{ from: 2, to: 0, label: 'registered: you can vote', dashed: true }
						],
						caption: 'The document proves a right to vote; the key, kept by you, will vote. Nothing ties either to the ballot.'
					},
					more: [
						[
							'A “zero-knowledge” proof?',
							[
								'It is a cryptographic technique that proves a statement is true without revealing the information that makes it true.',
								'An analogy: you want to prove to a bouncer you are over 18 without showing your card. A notary looks at your card in private and hands you an unforgeable “adult” certificate. Here the notary is a computation done by your phone, and anyone can check the certificate without trusting the phone.',
								'The proof relies on the State’s digital signature in the chip: a fake document cannot produce a valid proof.'
							]
						],
						[
							'The document’s identifier',
							[
								'It is computed from your document and the site’s name. The same document always gives the same identifier here: that is what guarantees one vote per document. It differs on every site and does not lead back to your name.',
								'We do not keep it as is: it goes through our one-way “mill”. If our database leaked, it would not give the list of documents.'
							]
						]
					]
				},
				{
					id: 'vote',
					title: '2. Voting',
					body: [
						'You rank up to 3 candidates by tapping them in order: the first is your favourite. Or you vote blank. You can change your mind at any time until voting closes: the last vote counts.',
						'Your browser downloads the whole list of voters (every fingerprint, not just yours: the request does not say who you are) and computes a Semaphore proof: “this ballot comes from one of the keys of the list”, without saying which. The proof also gives a period pseudonym, always the same for you over 30 days and impossible to tie to your key: that is what makes a new vote replace the old one.',
						'The ballot does not go to us: it is published on public Nostr relays run by others (nos.lol, relay.primal.net, offchain.pub, nostr.mom, relay.snort.social). Each one is signed by a throwaway key and dated to the hour. Our server reads the relays once an hour: it never sees a ballot’s IP address, nor the minute it was sent.',
						'Ballots are published in shuffled batches every hour (at least 3 at once, or after 6 hours of waiting at most), in the public log. If no relay answers, your browser sends the ballot straight to our server, which then sees the address it comes from.'
					],
					diagram: {
						actors: [{ label: 'Your browser' }, { label: 'Nostr relays', sub: 'public, run by others' }, { label: 'tartuffe.me' }, { label: 'Public log' }],
						steps: [
							{ from: 0, to: 2, label: 'downloads the whole list of voters' },
							{ at: 0, note: 'Proves “one of the keys of the list”, ties the proof to the ballot ①②③ and gives the period pseudonym.', span: 2 },
							{ from: 0, to: 1, label: 'ballot + proof, signed by a throwaway key, dated to the hour' },
							{ from: 1, to: 0, label: 'OK (several relays)', dashed: true },
							{ from: 2, to: 1, label: 'reads the new ballots, once an hour' },
							{ at: 2, note: 'Checks each proof. A new vote replaces the old one of the same pseudonym.', span: 2 },
							{ from: 2, to: 3, label: 'publishes the shuffled batch and the new totals' }
						],
						caption: 'Our server does not get the ballot from you: it reads it on the relays, with all the others.'
					},
					more: [
						[
							'Why 30-day periods?',
							[
								'A ballot counts for a 30-day period. Two series of periods run, the second 15 days later, and your browser votes in both: the results read the period that started first. It renews your ballot on its own when a new period opens, if you come back to the site.',
								'That is what lets a lost key be replaced without ever counting two votes for one document: a new key votes from the next period.'
							]
						]
					]
				},
				{
					id: 'comptage',
					title: '3. Two ways to count the same votes',
					body: [
						'“With the ranking” uses your 2nd and 3rd choices. Candidates are compared two by two: A comes before B when more voters rank A above B (a ranked candidate comes before an unranked one). The winner beats each other candidate head to head: the Condorcet winner. When none beats all the others, the Schulze method still gives an order.',
						'“One-round majority vote” keeps only the first choices, like the first round of an election. When the two winners differ, the page says so: the majority vote left out a preference of the majority.',
						'Results are hidden while there are too few voters, and the day before and the day of a vote, as the law requires. This is not an opinion poll: the voters are not representative of the population.'
					]
				},
				{
					id: 'blanc',
					title: '4. The blank vote counts',
					body: [
						'The blank vote is a ballot in its own right: “I came, I voted, none convinces me”. It excludes any ranking, counts among the voters, and so in every share. It has its own line in the results.',
						'Removing one’s vote is another thing: the ballot no longer counts at all.'
					]
				},
				{
					id: 'journal',
					title: '5. Checking, without trusting us',
					body: [
						'Every registration, every list of voters, every ballot and every total goes into a public log, built like those that watch the web’s certificates: a Merkle tree. Our server regularly signs the “head” of the tree (a checkpoint); a checkpoint commits to everything before it.',
						'Your browser keeps the last checkpoint it saw. At each visit, it asks for the proof that the new one extends the old one, erasing and rewriting nothing, then the proof that your ballot is in the tree, as you cast it. If not, it tells you and gives you the signed proof of the fault, to publish.',
						'At each batch, the checkpoint’s fingerprint is written into the Bitcoin blockchain, for free, by OpenTimestamps: nobody can later claim another log existed at that date. And anyone can download the whole log and recount everything with the open-source tool “dm poll verify”: the registrations, the lists of voters, every proof and every total, checked against the ballots left on the Nostr relays.'
					],
					diagram: {
						actors: [{ label: 'Your browser' }, { label: 'tartuffe.me' }, { label: 'Bitcoin', sub: 'OpenTimestamps' }, { label: 'Anyone' }],
						steps: [
							{ at: 1, note: 'Each entry becomes a leaf of the log’s Merkle tree.', span: 2 },
							{ from: 1, to: 2, label: 'fingerprint of the signed checkpoint, at each batch' },
							{ from: 0, to: 1, label: 'asks for the signed checkpoint' },
							{ from: 1, to: 0, label: 'size, root, signature + proof it extends the last one seen', dashed: true },
							{ from: 0, to: 1, label: 'asks for the proof that my ballot is in the tree' },
							{ at: 0, note: 'Checks: the log only grew, and it holds my ballot as I cast it. If not: a signed proof to download.', span: 2 },
							{ from: 3, to: 1, label: 'downloads the whole log' },
							{ at: 3, note: 'dm poll verify: replays registrations, lists, proofs and totals; compares with the relays.', span: 2 }
						],
						caption: 'We cannot change, erase or forget a ballot without it showing.'
					}
				},
				{
					id: 'appareils',
					title: '6. Several devices, and a lost key',
					body: [
						'Your voting key lives on your device. To find it elsewhere, three ways: a passkey (Face ID, fingerprint), which keeps with us a copy encrypted on your device that only your passkey opens; linking a device by QR code, like WhatsApp: an encrypted message from one device to the other, erased once read, within 10 minutes; or 12 recovery words, to write down.',
						'Lost everything? Scan your document again: a new key is made, and it votes from the next 30-day period. Until then, your old vote still counts. So a document never weighs two votes.'
					]
				}
			],
			getTitle: 'What we receive',
			get: [
				'“Adult: yes” and “French nationality: yes”, proved by the ZKPassport app.',
				'The HMAC fingerprint of your document’s identifier, for one voting key per document.',
				'The public fingerprint of your voting key. The key itself never leaves your devices.',
				'Anonymous ballots, read on the Nostr relays, with their proof. They are published, shuffled, in the public log.'
			],
			neverTitle: 'What we never see',
			never: [
				'Your name, your photo, your birth date, your document number.',
				'Who voted what: a ballot carries neither your document, nor your key, nor your account.',
				'The IP address of a ballot sent through the relays, nor the exact time it was sent.',
				'Your voting key in clear, your 12 words, your face or your fingerprint.'
			],
			limitsTitle: 'The limits, honestly',
			limitsLead: 'No online vote is perfect. Here is what this one does not guarantee.',
			limits: [
				'The Nostr relays see your browser’s IP address, like any site it opens; your internet provider sees you connect to them. There is no Tor option yet.',
				'If no relay answers, the ballot goes straight to us, and our server then sees its IP address (we do not keep it).',
				'A voter who wants to can prove their vote to someone: vote buying is not prevented. And whoever has your key, or your 12 words, votes in your place.',
				'This page’s code is served by us: a malicious server could serve other code. The code is open source, and the log can be checked by others.',
				'One server receives and publishes the ballots. The signed log anchored in Bitcoin makes cheating visible, not impossible.',
				'You need a smartphone with NFC and a chip document. The identifier belongs to the document, not the person: someone holding a valid passport and a valid ID card could vote twice. Renewing a document gives a new identifier.',
				'Whoever held a full copy of your document’s chip data could recompute its identifier: they would learn that this document has a voting key, never how it voted.',
				'The ZKPassport request goes through ZKPassport’s relay, end-to-end encrypted. ZKPassport is open source and had internal audits, no external audit yet.'
			],
			eraseTitle: 'Removing one’s vote, erasing one’s key',
			erase: [
				'Your vote is tied to no account: remove it on the vote page, with your key. A ballot not renewed expires after 30 days.',
				'“Remove the key from this device”, on your profile, erases it from this browser.'
			],
			techTitle: 'For the curious',
			tech: [
				'The ballot proofs are Semaphore proofs (Groth16), checked by our Go server with the verification keys of Semaphore’s public ceremony.',
				'The log is a Merkle tree in the sense of RFC 6962; its checkpoints are Ed25519 “signed notes” (C2SP format), served at /api/vote-log/checkpoint with their proofs.',
				'The ZKPassport check uses no ZKPassport account or key: our server runs the verifier of their open-source kit and only reads, on Ethereum, the public registry of the issuing States’ certificates.',
				'Recount it yourself: dm poll verify --api https://tartuffe.me --relays.'
			],
			links: [
				['ZKPassport source code (Apache-2.0)', 'https://github.com/zkpassport'],
				['Semaphore', 'https://semaphore.pse.dev'],
				['OpenTimestamps', 'https://opentimestamps.org']
			],
			cta: 'Go and vote'
		}
	};
	let c = $derived(content[currentLang]);
</script>

<svelte:head>
	<title>{c.kicker} — tartuffe.me</title>
	<meta name="description" content={c.lead} />
</svelte:head>

<article class="how">
	<header class="hero">
		<span class="kicker">{c.kicker}</span>
		<h1>{c.title}</h1>
		<p class="lead">{c.lead}</p>
	</header>

	<nav class="toc" aria-label={c.toc}>
		{#each c.parts as p (p.id)}<a href="#{p.id}">{p.title}</a>{/each}
		<a href="#limites">{c.limitsTitle}</a>
	</nav>

	{#each c.parts as part (part.id)}
		<section id={part.id} class="part">
			<h2>{part.title}</h2>
			{#if part.id === 'cle'}
				<div class="anim"><ZKProofAnimation lang={currentLang} /></div>
			{/if}
			{#each part.body as p}<p>{p}</p>{/each}
			{#if part.diagram}
				<SequenceDiagram actors={part.diagram.actors} steps={part.diagram.steps} caption={part.diagram.caption} />
			{/if}
			{#each part.more ?? [] as [title, paras]}
				<details class="more">
					<summary>{title}</summary>
					{#each paras as p}<p>{p}</p>{/each}
				</details>
			{/each}
		</section>
	{/each}

	<section class="part" id="donnees">
		<div class="split">
			<div class="box box--get">
				<h2>{c.getTitle}</h2>
				<ul>{#each c.get as line}<li>{line}</li>{/each}</ul>
			</div>
			<div class="box box--never">
				<h2>{c.neverTitle}</h2>
				<ul>{#each c.never as line}<li>{line}</li>{/each}</ul>
			</div>
		</div>
	</section>

	<section class="part" id="limites">
		<h2>{c.limitsTitle}</h2>
		<p>{c.limitsLead}</p>
		<ul class="limits">{#each c.limits as line}<li>{line}</li>{/each}</ul>
	</section>

	<section class="part">
		<h2>{c.eraseTitle}</h2>
		<ul>{#each c.erase as line}<li>{line}</li>{/each}</ul>
	</section>

	<details class="more" id="technique">
		<summary>{c.techTitle}</summary>
		{#each c.tech as p}<p>{p}</p>{/each}
		<ul>{#each c.links as [label, href]}<li><a {href} target="_blank" rel="noopener">{label}</a></li>{/each}</ul>
	</details>

	<p class="go"><a class="cta" href={paths.vote}>{c.cta} →</a></p>
</article>

<style>
	.how {
		max-width: 46rem;
		margin: 0 auto;
	}
	.hero {
		text-align: center;
		margin: 2rem 0 1.75rem;
	}
	.kicker {
		font-size: 0.75rem;
		font-weight: 600;
		letter-spacing: 0.14em;
		text-transform: uppercase;
		color: var(--event);
	}
	h1 {
		font-family: var(--font-serif);
		font-size: clamp(2rem, 6vw, 3rem);
		font-weight: 600;
		line-height: 1.08;
		margin: 0.4rem auto 0.9rem;
		text-wrap: balance;
	}
	.lead {
		font-size: 1.05rem;
		color: var(--text-secondary);
		line-height: 1.6;
		text-wrap: balance;
	}
	.toc {
		display: flex;
		flex-wrap: wrap;
		justify-content: center;
		gap: 0.4rem;
		margin: 0 0 2.5rem;
	}
	.toc a {
		padding: 0.25rem 0.7rem;
		border: 1px solid var(--glass-border);
		border-radius: var(--radius-full);
		background: var(--bg-card);
		color: var(--text-secondary);
		font-size: 0.78rem;
		text-decoration: none;
	}
	.toc a:hover {
		border-color: var(--event);
		color: var(--text-primary);
	}
	.part {
		margin-bottom: 2.75rem;
		scroll-margin-top: 5rem;
	}
	h2 {
		font-family: var(--font-serif);
		font-size: 1.7rem;
		font-weight: 600;
		margin: 0 0 0.8rem;
	}
	p,
	li {
		color: var(--text-secondary);
		line-height: 1.65;
		font-size: 0.97rem;
	}
	p {
		margin: 0 0 0.8rem;
	}
	ul {
		margin: 0;
		padding-left: 1.2rem;
		display: flex;
		flex-direction: column;
		gap: 0.45rem;
	}
	.anim {
		max-width: 460px;
		margin: 0 auto 1.25rem;
	}
	.more {
		padding: 0.8rem 0;
		border-top: 1px solid var(--glass-border);
	}
	.more:last-of-type {
		border-bottom: 1px solid var(--glass-border);
	}
	.more summary {
		font-family: var(--font-serif);
		font-size: 1.15rem;
		font-weight: 600;
		cursor: pointer;
	}
	.more[open] summary {
		margin-bottom: 0.6rem;
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
		font-size: 1.25rem;
	}
	.box--get {
		border-top: 2px solid var(--green);
	}
	.box--never {
		border-top: 2px solid var(--red);
	}
	.limits li::marker {
		color: var(--amber);
	}
	.go {
		text-align: center;
		margin: 2.5rem 0 1rem;
	}
	.cta {
		display: inline-block;
		padding: 0.75rem 1.6rem;
		border-radius: var(--radius-full);
		background: var(--event-strong);
		color: white;
		font-weight: 600;
		text-decoration: none;
	}
	.cta:hover {
		background: var(--event);
	}
	@media (max-width: 640px) {
		.split {
			grid-template-columns: 1fr;
		}
		h2 {
			font-size: 1.45rem;
		}
	}
</style>
