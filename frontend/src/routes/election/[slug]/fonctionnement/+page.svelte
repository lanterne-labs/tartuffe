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
			lead: 'Votre document prouve votre droit de vote. Votre bulletin ne porte ni votre document, ni votre compte, ni votre adresse. Et tout le monde peut recompter.',
			toc: 'Sommaire',
			parts: [
				{
					id: 'cle',
					title: '1. Obtenir sa clé de vote',
					body: [
						'Une personne, une voix : sans document, on pourrait créer cent comptes. Le passeport ou la carte d’identité à puce prouve seulement que vous êtes majeur et français.',
						'Votre navigateur crée une clé de vote secrète, qui ne le quitte jamais, et son empreinte publique. L’app ZKPassport lit la puce (NFC) et calcule sur le téléphone une preuve : « document authentique, signé par l’État, en cours de validité ; titulaire majeur et français ». La preuve est liée à l’empreinte de votre clé, et ne sert qu’une fois.',
						'Nous vérifions la preuve, puis gardons deux choses : une empreinte à sens unique de l’identifiant du document (HMAC, avec une clé secrète à nous), pour une seule clé de vote par document, et l’empreinte publique de votre clé, qui rejoint la liste des votants. Rien ne les relie à votre bulletin.'
					],
					diagram: {
						actors: [{ label: 'Votre navigateur' }, { label: 'App ZKPassport', sub: 'votre téléphone' }, { label: 'tartuffe.me' }, { label: 'Ethereum', sub: 'registre public' }],
						steps: [
							{ at: 0, note: 'Crée la clé secrète s et son empreinte publique C. s reste sur l’appareil.', span: 2 },
							{ from: 0, to: 2, label: 'demande un défi, sans compte ni session' },
							{ from: 2, to: 0, label: 'défi à usage unique', dashed: true },
							{ from: 0, to: 1, label: 'QR code : « majeur ? français ? », lié à H(défi, C)' },
							{ at: 1, note: 'Lit la puce (NFC), calcule la preuve. Les données restent sur le téléphone.', span: 2 },
							{ from: 1, to: 0, label: 'preuve : oui, oui. Ni nom ni numéro', dashed: true },
							{ from: 0, to: 2, label: 'preuve + C' },
							{ from: 2, to: 3, label: 'lit les certificats des États (lecture seule)' },
							{ at: 2, note: 'Vérifie la preuve, garde HMAC(document) et C. C rejoint la liste des votants.', span: 2 },
							{ from: 2, to: 0, label: 'inscrit : vous pouvez voter', dashed: true }
						],
						caption: 'Le document ouvre le droit de vote ; la clé, que vous seul avez, vote. Rien ne relie les deux au bulletin.'
					},
					more: [
						[
							'Une preuve « à divulgation nulle de connaissance » ?',
							[
								'C’est une preuve qu’une affirmation est vraie, qui ne révèle rien d’autre.',
								'C’est comme un certificat « majeur » infalsifiable, remis à la place de votre carte d’identité. Ici, c’est votre téléphone qui le calcule, et chacun peut le vérifier sans faire confiance au téléphone.',
								'La preuve repose sur la signature de l’État inscrite dans la puce : un faux document ne peut pas en produire.'
							]
						],
						[
							'L’identifiant du document',
							[
								'Il est calculé à partir du document et du nom du site : le même document donne toujours le même identifiant ici, d’où une voix par document. Il change d’un site à l’autre et ne mène pas à votre nom.',
								'Nous n’en gardons qu’une empreinte à sens unique : une fuite de notre base ne donnerait pas la liste des documents.'
							]
						]
					]
				},
				{
					id: 'vote',
					title: '2. Voter',
					body: [
						'Vous classez jusqu’à 3 candidats, par ordre de préférence, ou vous votez blanc. Vous pouvez changer d’avis jusqu’à la clôture : seul le dernier vote compte.',
						'Votre navigateur télécharge toute la liste des votants, pour ne pas dire qui vous êtes, et calcule une preuve Semaphore : « ce bulletin vient d’une clé de la liste », sans dire laquelle. Elle donne aussi un pseudonyme, le même pour vous pendant 30 jours et sans lien avec votre clé : un nouveau vote remplace ainsi l’ancien.',
						'Le bulletin ne passe pas par nous : il part sur des relais Nostr publics, tenus par d’autres (nos.lol, relay.primal.net, offchain.pub, nostr.mom, relay.snort.social), signé par une clé jetable et daté à l’heure près. Notre serveur lit les relais une fois par heure : il ne voit ni l’adresse IP d’un bulletin, ni la minute de son envoi.',
						'Chaque heure, les bulletins sont publiés par lots mélangés (au moins 3, ou après 6 h d’attente au plus) dans le registre public. Si aucun relais ne répond, le navigateur envoie le bulletin directement à notre serveur, qui voit alors son adresse.'
					],
					diagram: {
						actors: [{ label: 'Votre navigateur' }, { label: 'Relais Nostr', sub: 'publics, tenus par d’autres' }, { label: 'tartuffe.me' }, { label: 'Registre public' }],
						steps: [
							{ from: 0, to: 2, label: 'télécharge toute la liste des votants' },
							{ at: 0, note: 'Prouve « une clé de la liste », lie la preuve au bulletin ①②③, donne le pseudonyme.', span: 2 },
							{ from: 0, to: 1, label: 'bulletin + preuve, clé jetable, daté à l’heure' },
							{ from: 1, to: 0, label: 'OK (plusieurs relais)', dashed: true },
							{ from: 2, to: 1, label: 'lit les nouveaux bulletins, chaque heure' },
							{ at: 2, note: 'Vérifie chaque preuve. Un nouveau vote remplace l’ancien du même pseudonyme.', span: 2 },
							{ from: 2, to: 3, label: 'publie le lot mélangé et les totaux' }
						],
						caption: 'Notre serveur ne reçoit pas votre bulletin de vous : il le lit sur les relais, parmi les autres.'
					},
					more: [
						[
							'Pourquoi des périodes de 30 jours ?',
							[
								'Un bulletin vaut pour 30 jours. Deux séries de périodes tournent, décalées de 15 jours, et votre navigateur vote dans les deux ; les résultats lisent celle qui a commencé en premier. À chaque visite, il renouvelle votre bulletin si une nouvelle période s’est ouverte.',
								'C’est ce qui permet de remplacer une clé perdue sans jamais compter deux voix pour un document : la nouvelle clé vote à partir de la période suivante.'
							]
						]
					]
				},
				{
					id: 'comptage',
					title: '3. Les mêmes votes, deux comptages',
					body: [
						'« Avec le classement » : on oppose les candidats deux à deux. A passe devant B si plus de votants classent A au-dessus de B (un candidat classé passe devant un candidat qui ne l’est pas). Celui qui bat tous les autres est le gagnant de Condorcet ; s’il n’y en a pas, la méthode de Schulze donne tout de même un ordre.',
						'« Au scrutin majoritaire » : seuls les premiers choix comptent, comme au premier tour. Quand les deux gagnants diffèrent, la page le signale.',
						'Les résultats sont affichés dès le premier vote, sauf la veille et le jour du scrutin, comme l’exige la loi. Ce n’est pas un sondage : les votants ne sont pas représentatifs.'
					]
				},
				{
					id: 'blanc',
					title: '4. Le vote blanc compte',
					body: [
						'C’est un bulletin à part entière : « j’ai voté, mais aucun ne me convainc ». Il ne classe personne, mais il compte parmi les votants et dans chaque pourcentage, avec sa propre ligne.',
						'Retirer son vote, c’est autre chose : le bulletin ne compte plus.'
					]
				},
				{
					id: 'journal',
					title: '5. Vérifier, sans nous faire confiance',
					body: [
						'Inscriptions, listes de votants, bulletins et totaux entrent dans un registre public, un arbre de Merkle comme ceux qui surveillent les certificats du web. Notre serveur en signe régulièrement la tête (un point de contrôle), qui engage tout ce qui précède.',
						'Votre navigateur garde le dernier point de contrôle vu. À chaque visite, il vérifie que le nouveau prolonge l’ancien sans rien effacer, puis que votre bulletin y figure, intact. Sinon, il vous alerte et vous donne la preuve signée à publier.',
						'À chaque lot, l’empreinte du point de contrôle est horodatée dans Bitcoin par OpenTimestamps : impossible ensuite de prétendre à un autre registre à cette date. Et chacun peut tout télécharger et recompter avec l’outil open source « dm poll verify », en comparant avec les bulletins restés sur les relais.'
					],
					diagram: {
						actors: [{ label: 'Votre navigateur' }, { label: 'tartuffe.me' }, { label: 'Bitcoin', sub: 'OpenTimestamps' }, { label: 'N’importe qui' }],
						steps: [
							{ at: 1, note: 'Chaque entrée devient une feuille de l’arbre de Merkle.', span: 2 },
							{ from: 1, to: 2, label: 'empreinte du point de contrôle, à chaque lot' },
							{ from: 0, to: 1, label: 'demande le point de contrôle signé' },
							{ from: 1, to: 0, label: 'taille, racine, signature + preuve qu’il prolonge le précédent', dashed: true },
							{ from: 0, to: 1, label: 'demande la preuve que mon bulletin y est' },
							{ at: 0, note: 'Vérifie que le registre n’a fait que grandir et contient mon bulletin intact. Sinon : preuve signée à télécharger.', span: 2 },
							{ from: 3, to: 1, label: 'télécharge tout le registre' },
							{ at: 3, note: 'dm poll verify : rejoue inscriptions, listes, preuves et totaux, et compare avec les relais.', span: 2 }
						],
						caption: 'Changer, effacer ou oublier un bulletin se verrait.'
					}
				},
				{
					id: 'appareils',
					title: '6. Plusieurs appareils, clé perdue',
					body: [
						'Votre clé vit sur votre appareil. Vous pouvez la retrouver ailleurs de trois façons : une clé d’accès (Face ID, empreinte), qui ouvre une copie chiffrée gardée chez nous ; un autre appareil relié par QR code, comme WhatsApp (message chiffré, effacé dès sa lecture, valable 10 minutes) ; ou 12 mots de secours, à noter.',
						'Tout perdu ? Scannez à nouveau votre document : la nouvelle clé vote à partir de la période de 30 jours suivante. D’ici là, votre ancien vote compte. Un document ne pèse jamais deux voix.'
					]
				}
			],
			getTitle: 'Ce que nous recevons',
			get: [
				'« Majeur : oui », « nationalité française : oui », prouvés par ZKPassport.',
				'Une empreinte HMAC de l’identifiant du document : une clé de vote par document.',
				'L’empreinte publique de votre clé. La clé, elle, reste sur vos appareils.',
				'Des bulletins anonymes et leur preuve, lus sur les relais, publiés mélangés.'
			],
			neverTitle: 'Ce que nous ne voyons jamais',
			never: [
				'Votre nom, votre photo, votre date de naissance, le numéro du document.',
				'Qui a voté quoi : un bulletin ne porte ni votre document, ni votre clé, ni votre compte.',
				'L’adresse IP d’un bulletin passé par les relais, ni l’heure exacte de son envoi.',
				'Votre clé en clair, vos 12 mots, votre visage, votre empreinte.'
			],
			limitsTitle: 'Les limites, honnêtement',
			limitsLead: 'Ce que ce vote ne garantit pas.',
			limits: [
				'Les relais Nostr voient l’adresse IP de votre navigateur, comme tout site visité ; votre fournisseur d’accès voit que vous vous y connectez. Pas encore d’option Tor.',
				'Si aucun relais ne répond, le bulletin arrive directement chez nous, et nous voyons son adresse IP (sans la garder).',
				'Un votant peut prouver son vote à un tiers : l’achat de votes n’est pas empêché. Et qui a votre clé ou vos 12 mots vote à votre place.',
				'Nous servons le code de cette page : un serveur malveillant pourrait en servir un autre. Le code est open source, le registre vérifiable par d’autres.',
				'Un seul serveur reçoit et publie les bulletins. Le registre signé et horodaté dans Bitcoin rend la triche visible, pas impossible.',
				'Il faut un smartphone NFC et un document à puce. L’identifiant suit le document, pas la personne : avec un passeport et une carte d’identité valides, on pourrait voter deux fois. Un document renouvelé donne un nouvel identifiant.',
				'Qui aurait une copie complète de la puce de votre document pourrait recalculer son identifiant : il saurait que ce document a une clé de vote, jamais pour qui elle vote.',
				'La demande ZKPassport passe par leur relais, chiffrée de bout en bout. ZKPassport est open source, audité en interne, pas encore par un tiers.'
			],
			eraseTitle: 'Retirer son vote, effacer sa clé',
			erase: [
				'Votre vote n’est lié à aucun compte : retirez-le sur la page du vote, avec votre clé. Un bulletin non renouvelé expire au bout de 30 jours.',
				'« Retirer la clé de cet appareil », sur votre profil, l’efface de ce navigateur.'
			],
			techTitle: 'Pour les curieux',
			tech: [
				'Preuves de vote Semaphore (Groth16), vérifiées par notre serveur en Go avec les clés de la cérémonie publique de Semaphore.',
				'Registre : arbre de Merkle au sens de la RFC 6962 ; points de contrôle en « notes signées » Ed25519 (format C2SP), servis sur /api/vote-log/checkpoint avec leurs preuves.',
				'Vérification ZKPassport sans compte ni clé ZKPassport : notre serveur exécute le vérificateur de leur kit open source et lit seulement, sur Ethereum, le registre public des certificats des États.',
				'Recompter soi-même : dm poll verify --api https://tartuffe.me --relays.'
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
			title: 'A vote nobody can link to you. Not even us.',
			lead: 'Your document proves your right to vote. Your ballot carries neither your document, nor your account, nor your address. And anyone can recount.',
			toc: 'Contents',
			parts: [
				{
					id: 'cle',
					title: '1. Getting your voting key',
					body: [
						'One person, one vote: without a document, anyone could create a hundred accounts. The chip passport or ID card only proves that you are an adult and French.',
						'Your browser makes a secret voting key, which never leaves it, and its public fingerprint. The ZKPassport app reads the chip (NFC) and computes a proof on the phone: “genuine document, signed by the State, still valid; holder adult and French”. The proof is bound to your key’s fingerprint, and works once.',
						'We check the proof, then keep two things: a one-way fingerprint of the document identifier (HMAC, with a secret key of ours), for one voting key per document, and your key’s public fingerprint, which joins the voter list. Nothing links them to your ballot.'
					],
					diagram: {
						actors: [{ label: 'Your browser' }, { label: 'ZKPassport app', sub: 'your phone' }, { label: 'tartuffe.me' }, { label: 'Ethereum', sub: 'public registry' }],
						steps: [
							{ at: 0, note: 'Makes the secret key s and its public fingerprint C. s stays on the device.', span: 2 },
							{ from: 0, to: 2, label: 'asks for a challenge, no account, no session' },
							{ from: 2, to: 0, label: 'one-time challenge', dashed: true },
							{ from: 0, to: 1, label: 'QR code: “adult? French?”, bound to H(challenge, C)' },
							{ at: 1, note: 'Reads the chip (NFC), computes the proof. The data stays on the phone.', span: 2 },
							{ from: 1, to: 0, label: 'proof: yes, yes. No name, no number', dashed: true },
							{ from: 0, to: 2, label: 'proof + C' },
							{ from: 2, to: 3, label: 'reads the States’ certificates (read only)' },
							{ at: 2, note: 'Checks the proof, keeps HMAC(document) and C. C joins the voter list.', span: 2 },
							{ from: 2, to: 0, label: 'registered: you can vote', dashed: true }
						],
						caption: 'The document grants the right to vote; the key, which only you hold, votes. Nothing links the two to the ballot.'
					},
					more: [
						[
							'A “zero-knowledge” proof?',
							[
								'A proof that a claim is true, revealing nothing else.',
								'Like a forgery-proof “adult” certificate handed over instead of your ID card. Here your phone computes it, and anyone can check it without trusting the phone.',
								'The proof rests on the State’s signature in the chip: a fake document cannot produce one.'
							]
						],
						[
							'The document identifier',
							[
								'Computed from the document and the site’s name: the same document always gives the same identifier here, hence one vote per document. It differs from site to site and does not lead to your name.',
								'We only keep a one-way fingerprint of it: a leak of our database would not give the list of documents.'
							]
						]
					]
				},
				{
					id: 'vote',
					title: '2. Voting',
					body: [
						'Up to 3 candidates, in order of preference, or a blank vote. You can change it until voting closes: the last vote counts.',
						'Your browser downloads the whole voter list, so as not to say who you are, and computes a Semaphore proof: “this ballot comes from a key on the list”, without saying which. It also gives a pseudonym, the same for you for 30 days and unlinked to your key: that is how a new vote replaces the old one.',
						'The ballot does not go through us: it goes out to public Nostr relays run by others (nos.lol, relay.primal.net, offchain.pub, nostr.mom, relay.snort.social), signed by a throwaway key and dated to the hour. Our server reads the relays once an hour: it sees neither a ballot’s IP address nor the minute it was sent.',
						'Every hour, ballots are published in shuffled batches (at least 3, or after 6 h of waiting at most) in the public ledger. If no relay answers, the browser sends the ballot straight to our server, which then sees its address.'
					],
					diagram: {
						actors: [{ label: 'Your browser' }, { label: 'Nostr relays', sub: 'public, run by others' }, { label: 'tartuffe.me' }, { label: 'Public ledger' }],
						steps: [
							{ from: 0, to: 2, label: 'downloads the whole voter list' },
							{ at: 0, note: 'Proves “a key on the list”, binds the proof to the ballot ①②③, gives the pseudonym.', span: 2 },
							{ from: 0, to: 1, label: 'ballot + proof, throwaway key, dated to the hour' },
							{ from: 1, to: 0, label: 'OK (several relays)', dashed: true },
							{ from: 2, to: 1, label: 'reads the new ballots, every hour' },
							{ at: 2, note: 'Checks each proof. A new vote replaces the old one of the same pseudonym.', span: 2 },
							{ from: 2, to: 3, label: 'publishes the shuffled batch and the totals' }
						],
						caption: 'Our server does not get your ballot from you: it reads it on the relays, among the others.'
					},
					more: [
						[
							'Why 30-day periods?',
							[
								'A ballot holds for 30 days. Two series of periods run, 15 days apart, and your browser votes in both; the results read the one that started first. On each visit, it renews your ballot if a new period has opened.',
								'That is how a lost key can be replaced without ever counting two votes for one document: the new key votes from the next period.'
							]
						]
					]
				},
				{
					id: 'comptage',
					title: '3. The same votes, two counts',
					body: [
						'“With the ranking”: candidates are matched in pairs. A beats B if more voters rank A above B (a ranked candidate beats an unranked one). The one who beats all others is the Condorcet winner; if there is none, the Schulze method still gives an order.',
						'“One-round majority vote”: only first choices count, as in a first round. When the two winners differ, the page says so.',
						'Results shown from the first vote, except the day before and the day of the vote, as the law requires. This is not an opinion poll: the voters are not representative.'
					]
				},
				{
					id: 'blanc',
					title: '4. The blank vote counts',
					body: [
						'A ballot in its own right: “I voted, none convinces me”. No ranking, counted among the voters and in every share, with its own line.',
						'Withdrawing your vote is something else: the ballot no longer counts.'
					]
				},
				{
					id: 'journal',
					title: '5. Checking, without trusting us',
					body: [
						'Registrations, voter lists, ballots and totals go into a public ledger, a Merkle tree like those that watch the web’s certificates. Our server regularly signs its head (a checkpoint), which commits to everything before it.',
						'Your browser keeps the last checkpoint it saw. On each visit, it checks that the new one extends the old without erasing anything, then that your ballot is in it, intact. If not, it warns you and gives you the signed proof to publish.',
						'At each batch, the checkpoint’s fingerprint is timestamped in Bitcoin by OpenTimestamps: no one can later claim another ledger at that date. And anyone can download everything and recount with the open-source tool “dm poll verify”, comparing with the ballots left on the relays.'
					],
					diagram: {
						actors: [{ label: 'Your browser' }, { label: 'tartuffe.me' }, { label: 'Bitcoin', sub: 'OpenTimestamps' }, { label: 'Anyone' }],
						steps: [
							{ at: 1, note: 'Each entry becomes a leaf of the Merkle tree.', span: 2 },
							{ from: 1, to: 2, label: 'checkpoint fingerprint, at each batch' },
							{ from: 0, to: 1, label: 'asks for the signed checkpoint' },
							{ from: 1, to: 0, label: 'size, root, signature + proof it extends the previous one', dashed: true },
							{ from: 0, to: 1, label: 'asks for the proof my ballot is in it' },
							{ at: 0, note: 'Checks the ledger only grew and holds my ballot intact. If not: signed proof to download.', span: 2 },
							{ from: 3, to: 1, label: 'downloads the whole ledger' },
							{ at: 3, note: 'dm poll verify: replays registrations, lists, proofs and totals, and compares with the relays.', span: 2 }
						],
						caption: 'Changing, erasing or forgetting a ballot would show.'
					}
				},
				{
					id: 'appareils',
					title: '6. Several devices, a lost key',
					body: [
						'Your key lives on your device. Three ways to get it elsewhere: a passkey (Face ID, fingerprint), which opens an encrypted copy we keep; another device linked by QR code, as with WhatsApp (encrypted message, erased once read, valid 10 minutes); or 12 recovery words, to write down.',
						'Lost everything? Scan your document again: the new key votes from the next 30-day period. Until then, your old vote counts. A document never weighs two votes.'
					]
				}
			],
			getTitle: 'What we receive',
			get: [
				'“Adult: yes”, “French nationality: yes”, proved by ZKPassport.',
				'An HMAC fingerprint of the document identifier: one voting key per document.',
				'Your key’s public fingerprint. The key itself stays on your devices.',
				'Anonymous ballots and their proof, read on the relays, published shuffled.'
			],
			neverTitle: 'What we never see',
			never: [
				'Your name, your photo, your birth date, the document number.',
				'Who voted what: a ballot carries neither your document, nor your key, nor your account.',
				'The IP address of a ballot sent through the relays, nor the exact time it was sent.',
				'Your key in clear, your 12 words, your face, your fingerprint.'
			],
			limitsTitle: 'The limits, honestly',
			limitsLead: 'What this vote does not guarantee.',
			limits: [
				'The Nostr relays see your browser’s IP address, as any site visited does; your internet provider sees you connect to them. No Tor option yet.',
				'If no relay answers, the ballot comes straight to us, and we see its IP address (we do not keep it).',
				'A voter can prove their vote to someone: vote buying is not prevented. And whoever has your key or your 12 words votes in your place.',
				'We serve this page’s code: a malicious server could serve other code. The code is open source, the ledger checkable by others.',
				'A single server receives and publishes the ballots. The ledger, signed and timestamped in Bitcoin, makes cheating visible, not impossible.',
				'You need an NFC smartphone and a chip document. The identifier follows the document, not the person: with a valid passport and ID card, one could vote twice. A renewed document gives a new identifier.',
				'Someone with a full copy of your document’s chip could recompute its identifier: they would know this document has a voting key, never whom it votes for.',
				'The ZKPassport request goes through their relay, end-to-end encrypted. ZKPassport is open source, audited internally, not yet by a third party.'
			],
			eraseTitle: 'Withdrawing your vote, erasing your key',
			erase: [
				'Your vote is tied to no account: withdraw it on the vote page, with your key. A ballot not renewed expires after 30 days.',
				'“Remove the key from this device”, on your profile, erases it from this browser.'
			],
			techTitle: 'For the curious',
			tech: [
				'The ballot proofs are Semaphore proofs (Groth16), checked by our Go server with the verification keys of Semaphore’s public ceremony.',
				'The ledger is a Merkle tree in the sense of RFC 6962; its checkpoints are Ed25519 “signed notes” (C2SP format), served at /api/vote-log/checkpoint with their proofs.',
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
