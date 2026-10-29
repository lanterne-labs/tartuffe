<script lang="ts">
	// How the event's vote works (tartuffe-vote/2, docs/vote/SPEC.md), for
	// every reader: the voting key and the passport check (ZKPassport), the
	// encrypted ballot and its way through the relays, the count of sums,
	// the blank vote, the public log and its checks, the devices, what the
	// site receives and never sees, the levels of privacy and what holds at
	// every level. Every line must stay true to the code: backend/vote,
	// circuits/ballot, frontend/src/lib/vote.
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
			briefTitle: string;
			brief: string[];
			levelsMore: string;
			toc: string;
			parts: Part[];
			getTitle: string;
			get: string[];
			neverTitle: string;
			never: string[];
			levelsTitle: string;
			levelsLead: string;
			levels: { name: string; how: string; lines: string[] }[];
			knownTitle: string;
			known: string[];
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
			title: 'Un vote que personne ne peut lire. Pas même nous.',
			lead: 'Votre passeport prouve votre droit de participer. Votre bulletin part chiffré, sans votre nom, votre clé ni votre adresse. Seuls les totaux sont déchiffrés, avec une preuve, et tout le monde peut les recompter.',
			briefTitle: 'En bref',
			brief: [
				'Votre passeport prouve, une seule fois et sur votre téléphone, que vous êtes majeur et français. Nous ne voyons ni votre nom ni votre photo.',
				'Vous classez jusqu’à 3 candidats, ou votez blanc, et pouvez changer d’avis jusqu’à la clôture.',
				'Votre bulletin part chiffré, sans votre nom ni votre adresse : personne, pas même nous, ne peut le lire.',
				'Seuls les totaux sont déchiffrés, après chaque vote, et tout le monde peut les recompter.',
				'C’est une expérience de tartuffe.me, site indépendant : ni un scrutin officiel, ni un sondage représentatif.'
			],
			levelsMore: 'Lire le détail',
			toc: 'Sommaire',
			parts: [
				{
					id: 'cle',
					title: '1. Obtenir sa clé de vote',
					body: [
						'Un passeport, une clé, une voix : sans passeport, on pourrait créer cent clés. Le passeport ne sert qu’à prouver que vous êtes majeur et français ; c’est la clé qui vote.',
						'Votre navigateur crée la clé : à partir de Face ID ou de votre empreinte (une « clé d’accès »), qui la retrouve sur vos autres appareils, ou de 12 mots à noter. Il en calcule l’empreinte publique, la seule chose qui sortira de l’appareil.',
						'L’app ZKPassport lit la puce (NFC) et calcule sur le téléphone une preuve : « passeport authentique, signé par l’État, en cours de validité ; titulaire majeur et français », liée à l’empreinte de votre clé. Elle ne révèle ni nom ni numéro, seulement un identifiant propre au passeport et à ce site.',
						'L’inscription part sur un relais public, pas chez nous. Notre serveur la lit, vérifie la preuve et l’inscrit dans le registre public, preuve comprise : chacun peut vérifier que chaque clé de la liste des votants vient d’un vrai passeport, et qu’aucun passeport n’en a deux. Quelques secondes plus tard, vous pouvez voter.'
					],
					diagram: {
						actors: [{ label: 'Votre navigateur' }, { label: 'App ZKPassport', sub: 'votre téléphone' }, { label: 'Relais', sub: 'publics, tenus par d’autres' }, { label: 'tartuffe.me' }, { label: 'Registre public' }],
						steps: [
							{ at: 0, note: 'Crée la clé secrète s (Face ID, empreinte ou 12 mots) et son empreinte publique.', span: 2 },
							{ from: 0, to: 1, label: 'QR code : « majeur ? français ? passeport valide ? », lié à l’empreinte' },
							{ at: 1, note: 'Lit la puce, calcule la preuve. Les données restent sur le téléphone.', span: 2 },
							{ from: 1, to: 0, label: 'preuve : oui, oui, oui. Ni nom ni numéro', dashed: true },
							{ from: 0, to: 2, label: 'inscription : empreinte + preuve' },
							{ from: 3, to: 2, label: 'lit les inscriptions' },
							{ at: 3, note: 'Vérifie la preuve, un passeport = une clé.', span: 2 },
							{ from: 3, to: 4, label: 'inscription publiée, preuve comprise' },
							{ from: 3, to: 2, label: 'accusé de réception signé', dashed: true }
						],
						caption: 'Le passeport ouvre le vote ; c’est la clé, que vous êtes seul à détenir, qui vote.'
					},
					more: [
						[
							'Une preuve « à divulgation nulle de connaissance » ?',
							[
								'C’est une preuve qu’une affirmation est vraie, qui ne révèle rien d’autre.',
								'Imaginez qu’au lieu de montrer votre carte d’identité, vous remettiez un certificat infalsifiable qui dit seulement « majeur ». Ici, c’est votre téléphone qui le calcule, et chacun peut le vérifier sans avoir à faire confiance au téléphone.',
								'La preuve repose sur la signature de l’État inscrite dans la puce : un faux passeport ne peut pas en produire.'
							]
						],
						[
							'L’identifiant du passeport',
							[
								'Il est calculé à partir des données de la puce et du nom du site : le même passeport donne toujours le même identifiant ici, d’où une clé par passeport. Il change d’un site à l’autre et ne mène pas à votre nom.',
								'Il est public, dans le registre : c’est ce qui permet à chacun de vérifier qu’aucun passeport n’a deux clés. L’État, qui connaît les puces qu’il a signées, pourrait donc savoir qu’un passeport s’est inscrit. Jamais pour qui il vote.'
							]
						]
					]
				},
				{
					id: 'vote',
					title: '2. Voter',
					body: [
						'Vous classez jusqu’à 3 candidats, par ordre de préférence, ou vous votez blanc. Vous pouvez changer d’avis jusqu’à la clôture : seul le dernier bulletin compte.',
						'Votre navigateur chiffre le bulletin avec la clé de l’élection, puis calcule une preuve (une seconde environ) : « ce bulletin vient d’une clé de la liste des votants, sans dire laquelle ; il respecte les règles ; il est chiffré correctement ». La preuve porte aussi votre pseudonyme, le même pour tous vos bulletins et sans lien avec votre clé : un nouveau bulletin remplace l’ancien.',
						'Le bulletin ne passe pas par nous : il part sur un relais public, signé par une clé jetable. Notre serveur lit les relais, vérifie la preuve et ajoute le bulletin, toujours chiffré, au registre public. Toutes les 20 secondes, il signe l’état du registre (un point de contrôle).',
						'Sur la page, « Vote enregistré » apparaît en quelques secondes, puis « Vote enregistré et vérifié » quand votre navigateur l’a retrouvé dans le registre public, intact. Il ne demande jamais « mon bulletin » à notre serveur : il lit la même liste que tout le monde.'
					],
					diagram: {
						actors: [{ label: 'Votre navigateur' }, { label: 'Relais', sub: 'publics, tenus par d’autres' }, { label: 'tartuffe.me' }, { label: 'Registre public' }],
						steps: [
							{ at: 0, note: 'Chiffre le classement, prouve « une clé de la liste » et les règles, donne le pseudonyme.', span: 2 },
							{ from: 0, to: 1, label: 'bulletin chiffré + preuve, clé jetable' },
							{ from: 2, to: 1, label: 'lit les nouveaux bulletins' },
							{ at: 2, note: 'Vérifie la preuve. Un nouveau bulletin remplace l’ancien du même pseudonyme.', span: 2 },
							{ from: 2, to: 1, label: 'accusé de réception signé', dashed: true },
							{ from: 2, to: 3, label: 'bulletin chiffré + point de contrôle signé' }
						],
						caption: 'Notre serveur ne reçoit pas votre bulletin de vous : il le lit sur les relais, parmi les autres.'
					}
				},
				{
					id: 'comptage',
					title: '3. Compter sans lire',
					body: [
						'Les bulletins chiffrés s’additionnent sans être ouverts. Pour publier des résultats, notre serveur ne déchiffre que la somme, et joint une preuve que ce déchiffrement est le bon : chacun peut refaire l’addition à partir du registre et vérifier la preuve.',
						'Un réglage de l’élection, K_REAL, fixe combien de bulletins modifiés déclenchent une mise à jour des résultats. Au lancement il vaut 1 : les résultats bougent après chaque vote, avec un court délai aléatoire. Une mise à jour révèle alors la différence avec la précédente, c’est-à-dire ce bulletin, sans lien avec son auteur : le registre ne relie le pseudonyme ni au passeport ni à une adresse ; seul le serveur de transport qui a reçu le bulletin voit une adresse et une heure. Un réglage plus haut ferait de chaque mise à jour une somme de plusieurs bulletins que personne ne peut démêler, au prix de l’attente.',
						'« Avec le classement » : on oppose les candidats deux à deux à partir des totaux. Celui qui bat tous les autres est le gagnant de Condorcet ; s’il n’y en a pas, la méthode de Schulze donne tout de même un ordre. Les premiers choix sont affichés aussi, comme au premier tour.',
						'La clé de l’élection est chez nous, sur une machine sans accès entrant. Elle pourrait déchiffrer un bulletin seul : c’est la limite de ce vote, dite plus bas. Elle est détruite 30 jours après la clôture.',
						'Les résultats sont affichés dès le premier lot, sauf la veille et le jour du scrutin, comme l’exige la loi. Ce n’est pas un sondage : les votants ne sont pas représentatifs.'
					]
				},
				{
					id: 'blanc',
					title: '4. Le vote blanc compte',
					body: [
						'C’est un bulletin à part entière : « j’ai voté, mais aucun ne me convainc ». Il ne classe personne, mais il compte parmi les votants et dans chaque pourcentage, avec sa propre ligne.',
						'Vous pouvez aussi le placer après vos candidats : il signifie alors « les autres, non ». Il affronte chaque candidat en duel, comme s’il en était un : un candidat que la majorité place sous le vote blanc passe derrière lui dans le classement.',
						'Retirer son vote, c’est autre chose : le bulletin ne compte plus. Dans le registre, un retrait ressemble à n’importe quel bulletin.'
					]
				},
				{
					id: 'journal',
					title: '5. Vérifier, sans nous faire confiance',
					body: [
						'Inscriptions, bulletins chiffrés et totaux entrent dans un registre public, un arbre de Merkle comme ceux qui surveillent les certificats du web. Notre serveur en signe l’état toutes les 20 secondes ; chaque point de contrôle engage tout ce qui précède, et son empreinte est horodatée dans Bitcoin : impossible ensuite de faire passer un autre registre pour celui de cette date.',
						'Votre navigateur garde les points de contrôle qu’il a vus et vérifie que chaque nouveau prolonge l’ancien sans rien effacer, puis que votre bulletin figure dans le registre, tel qu’envoyé.',
						'Chacun peut tout télécharger et tout recompter avec l’outil open source « dm vote verify » : les signatures, l’arbre, chaque preuve de passeport, chaque preuve de bulletin, l’addition des bulletins, les preuves de déchiffrement et les résultats.'
					],
					diagram: {
						actors: [{ label: 'Votre navigateur' }, { label: 'tartuffe.me' }, { label: 'Bitcoin', sub: 'OpenTimestamps' }, { label: 'N’importe qui' }],
						steps: [
							{ at: 1, note: 'Chaque entrée devient une feuille de l’arbre de Merkle.', span: 2 },
							{ from: 1, to: 2, label: 'empreinte du point de contrôle, toutes les 20 s' },
							{ at: 0, note: 'Vérifie que le registre n’a fait que grandir et contient mon bulletin, chiffré, intact.', span: 2 },
							{ from: 3, to: 1, label: 'télécharge tout le registre' },
							{ at: 3, note: 'dm vote verify : rejoue inscriptions, preuves, additions et déchiffrements.', span: 2 }
						],
						caption: 'Changer, effacer ou oublier un bulletin se verrait.'
					}
				},
				{
					id: 'appareils',
					title: '6. Plusieurs appareils, clé perdue',
					body: [
						'Votre clé vient de Face ID ou de votre empreinte, que votre téléphone retrouve sur vos autres appareils ; ou de 12 mots, à noter avant le premier bulletin. Avec l’un ou l’autre, vous changez votre bulletin depuis n’importe quel appareil, jusqu’à la clôture.',
						'Tout perdu, clé et copies ? Votre dernier bulletin reste compté, tel quel. Un passeport ne donne qu’une clé, et personne ne peut relier un bulletin à sa clé : nous ne pouvons donc ni l’annuler ni vous en donner une seconde. Un passeport ne compte jamais pour deux voix.'
					]
				}
			],
			getTitle: 'Ce que nous recevons',
			get: [
				'« Majeur, français, passeport valide : oui », prouvés par ZKPassport.',
				'L’identifiant du passeport pour ce site, et l’empreinte publique de votre clé. La clé, elle, reste sur vos appareils.',
				'Des bulletins chiffrés et leur preuve, lus sur les relais.',
				'La somme des bulletins, déchiffrée à chaque mise à jour.'
			],
			neverTitle: 'Ce que nous ne voyons jamais',
			never: [
				'Votre nom, votre photo, votre date de naissance, le numéro du passeport.',
				'L’adresse IP de votre navigateur : il ne nous parle jamais, ni pour s’inscrire, ni pour voter, ni pour vérifier.',
				'Quel bulletin est le vôtre : un bulletin ne porte ni votre passeport, ni votre clé, ni votre compte.',
				'Votre clé en clair, vos 12 mots, votre visage, votre empreinte.'
			],
			levelsTitle: 'Votre niveau de confidentialité',
			levelsLead: 'Le vote est anonyme pour tous. Pour aller plus loin, à vous de choisir.',
			levels: [
				{
					name: 'Par défaut',
					how: 'Rien à faire.',
					lines: [
						'Personne ne peut lire votre bulletin, sauf la clé de l’élection, qui ne sait pas de qui il vient.',
						'Notre serveur ne voit jamais votre adresse IP : vos envois passent par les relais, vos lectures par un miroir public.',
						'Les relais voient l’adresse IP de votre navigateur, comme tout site visité, et un bulletin chiffré qu’ils ne peuvent pas lire.',
						'L’État, qui connaît les puces de ses passeports, pourrait savoir que le vôtre s’est inscrit. Jamais pour qui il vote.'
					]
				},
				{
					name: 'Avec Tor',
					how: 'Ouvrez tartuffe.me dans Tor Browser, au niveau de sécurité « Standard » (les deux autres bloquent le calcul de la preuve).',
					lines: [
						'Les relais et le miroir ne voient qu’une adresse du réseau Tor.',
						'Votre fournisseur d’accès ne voit pas que vous votez.',
						'Pas de Face ID ni d’empreinte dans Tor Browser : notez les 12 mots, à saisir à chaque visite. Comptez quelques secondes de plus pour la preuve.'
					]
				}
			],
			knownTitle: 'À tous les niveaux',
			known: [
				'Un votant peut prouver son vote à un tiers, en révélant le secret qui l’a chiffré : l’achat de votes n’est pas empêché. Qui a votre clé ou vos 12 mots vote à votre place.',
				'La clé de l’élection, chez nous, pourrait déchiffrer un bulletin seul, sans savoir de qui il vient ; nous publions seulement les sommes, avec leurs preuves.',
				'Nous servons le code de cette page. Il est open source, et le registre peut être vérifié par d’autres que nous.',
				'Un seul serveur lit les relais et écrit le registre. Le registre signé et horodaté dans Bitcoin rend toute triche visible.',
				'Il faut un smartphone NFC et un passeport à puce. La carte d’identité n’est pas acceptée pour l’instant : avec les deux, une même personne aurait deux voix.',
				'Qui aurait une copie complète de la puce de votre passeport pourrait l’inscrire avant vous : vous trouveriez alors votre passeport déjà pris.',
				'La demande ZKPassport passe par leur relais, chiffrée de bout en bout. ZKPassport est open source, audité en interne, pas encore par un tiers.'
			],
			eraseTitle: 'Retirer son vote, effacer sa clé',
			erase: [
				'Votre vote n’est lié à aucun compte : retirez-le sur la page du vote, avec votre clé. Dans le registre, un retrait ressemble à un bulletin.',
				'« Retirer la clé de cet appareil », dans le menu « Ma clé » de la page du vote, l’efface de ce navigateur ; Face ID ou empreinte (« Retrouver ma clé »), vos 12 mots ou votre autre appareil la retrouvent.'
			],
			techTitle: 'Pour les curieux',
			tech: [
				'Bulletin : chiffrement ElGamal exponentiel sur la courbe Grumpkin, 10 chiffrés pour 66 valeurs (premiers choix, candidats classés, duels), preuve UltraHonk d’un circuit Noir (15 000 contraintes, sans cérémonie à nous : le setup universel d’Aztec).',
				'Liste des votants : arbre de Merkle Poseidon2 de profondeur 20 ; le navigateur met son chemin à jour à partir de fichiers publics, sans jamais demander sa propre feuille.',
				'Totaux : preuves de Chaum-Pedersen pour chaque composante ; classement par la méthode de Schulze sur les marges.',
				'Registre : arbre de Merkle au sens de la RFC 6962 ; points de contrôle en « notes signées » Ed25519 (format C2SP), horodatés par OpenTimestamps.',
				'Recompter soi-même : dm vote verify presidentielle-2027 --api https://tartuffe.me. La spécification complète : docs/vote/SPEC.md.'
			],
			links: [
				['Code de ZKPassport (Apache-2.0)', 'https://github.com/zkpassport'],
				['Noir', 'https://noir-lang.org'],
				['OpenTimestamps', 'https://opentimestamps.org']
			],
			cta: 'Aller voter'
		},
		en: {
			kicker: 'How it works',
			title: 'A vote nobody can read. Not even us.',
			lead: 'Your passport proves your right to take part. Your ballot leaves encrypted, without your name, your key or your address. Only the totals are decrypted, with a proof, and anyone can recount them.',
			briefTitle: 'In short',
			brief: [
				'Your passport proves, once and on your phone, that you are an adult French national. We see neither your name nor your photo.',
				'You rank up to 3 candidates, or vote blank, and can change your mind until the close.',
				'Your ballot leaves encrypted, without your name or address: nobody, not even us, can read it.',
				'Only the totals are decrypted, after every vote, and anyone can recount them.',
				'It is an experiment by tartuffe.me, an independent site: neither an official election nor a representative poll.'
			],
			levelsMore: 'Read the detail',
			toc: 'Contents',
			parts: [
				{
					id: 'cle',
					title: '1. Getting your voting key',
					body: [
						'One passport, one key, one vote: without a passport, anyone could make a hundred keys. The passport only proves that you are an adult and French; the key is what votes.',
						'Your browser makes the key: from Face ID or your fingerprint (a “passkey”), which brings it to your other devices, or from 12 words to write down. It computes the key’s public fingerprint, the only thing that leaves the device.',
						'The ZKPassport app reads the chip (NFC) and computes a proof on the phone: “genuine passport, signed by the State, still valid; holder adult and French”, bound to your key’s fingerprint. It reveals neither name nor number, only an identifier specific to the passport and to this site.',
						'The registration goes to a public relay, not to us. Our server reads it, checks the proof and writes it to the public log, proof included: anyone can check that every key in the voter list comes from a real passport, and that no passport has two. A few seconds later, you can vote.'
					],
					diagram: {
						actors: [{ label: 'Your browser' }, { label: 'ZKPassport app', sub: 'your phone' }, { label: 'Relays', sub: 'public, run by others' }, { label: 'tartuffe.me' }, { label: 'Public log' }],
						steps: [
							{ at: 0, note: 'Makes the secret key s (Face ID, fingerprint or 12 words) and its public fingerprint.', span: 2 },
							{ from: 0, to: 1, label: 'QR code: “adult? French? valid passport?”, bound to the fingerprint' },
							{ at: 1, note: 'Reads the chip, computes the proof. The data stays on the phone.', span: 2 },
							{ from: 1, to: 0, label: 'proof: yes, yes, yes. No name, no number', dashed: true },
							{ from: 0, to: 2, label: 'registration: fingerprint + proof' },
							{ from: 3, to: 2, label: 'reads the registrations' },
							{ at: 3, note: 'Checks the proof; one passport, one key.', span: 2 },
							{ from: 3, to: 4, label: 'registration published, proof included' },
							{ from: 3, to: 2, label: 'signed acknowledgement', dashed: true }
						],
						caption: 'The passport opens the vote; the key, which you alone hold, votes.'
					},
					more: [
						[
							'A “zero-knowledge” proof?',
							[
								'A proof that a statement is true, which reveals nothing else.',
								'Imagine handing over, instead of your ID card, a forgery-proof certificate that says only “adult”. Here your phone computes it, and anyone can check it without trusting the phone.',
								'The proof rests on the State’s signature in the chip: a fake passport cannot produce one.'
							]
						],
						[
							'The passport identifier',
							[
								'It is computed from the chip’s data and the site’s name: the same passport always gives the same identifier here, hence one key per passport. It changes from site to site and does not lead to your name.',
								'It is public, in the log: that is what lets anyone check that no passport has two keys. The State, which knows the chips it signed, could therefore tell that a passport registered. Never whom it votes for.'
							]
						]
					]
				},
				{
					id: 'vote',
					title: '2. Voting',
					body: [
						'You rank up to 3 candidates, in order of preference, or vote blank. You can change your mind until the close: only the last ballot counts.',
						'Your browser encrypts the ballot with the election’s key, then computes a proof (about a second): “this ballot comes from a key of the voter list, without saying which; it follows the rules; it is correctly encrypted”. The proof also carries your pseudonym, the same for all your ballots and unlinked to your key: a new ballot replaces the old one.',
						'The ballot does not go through us: it goes to a public relay, signed by a throwaway key. Our server reads the relays, checks the proof and adds the ballot, still encrypted, to the public log. Every 20 seconds it signs the log’s state (a checkpoint).',
						'On the page, “Vote recorded” appears within seconds, then “Vote recorded and checked” once your browser has found it in the public log, intact. It never asks our server for “my ballot”: it reads the same list as everyone.'
					],
					diagram: {
						actors: [{ label: 'Your browser' }, { label: 'Relays', sub: 'public, run by others' }, { label: 'tartuffe.me' }, { label: 'Public log' }],
						steps: [
							{ at: 0, note: 'Encrypts the ranking, proves “a key of the list” and the rules, gives the pseudonym.', span: 2 },
							{ from: 0, to: 1, label: 'encrypted ballot + proof, throwaway key' },
							{ from: 2, to: 1, label: 'reads the new ballots' },
							{ at: 2, note: 'Checks the proof. A new ballot replaces the old one of the same pseudonym.', span: 2 },
							{ from: 2, to: 1, label: 'signed acknowledgement', dashed: true },
							{ from: 2, to: 3, label: 'encrypted ballot + signed checkpoint' }
						],
						caption: 'Our server does not receive your ballot from you: it reads it on the relays, among the others.'
					}
				},
				{
					id: 'comptage',
					title: '3. Counting without reading',
					body: [
						'Encrypted ballots add up without being opened. To publish results, our server decrypts only the sum, and attaches a proof that this decryption is right: anyone can redo the addition from the log and check the proof.',
						'A setting of the election, K_REAL, says how many changed ballots trigger a results update. At launch it is 1: results move after every vote, with a short random delay. An update then reveals the difference from the previous one, that is, that ballot, with no link to its author: the log ties the pseudonym neither to the passport nor to an address; only the transport server that received the ballot sees an address and a time. A higher setting would make every update a sum of several ballots nobody can untangle, at the price of waiting.',
						'“With the ranking”: the candidates are set against each other in pairs from the totals. The one who beats every other is the Condorcet winner; if there is none, the Schulze method still gives an order. First choices are shown too, as in a first round.',
						'The election key is with us, on a machine with no inbound access. It could decrypt a single ballot: that is the limit of this vote, stated below. It is destroyed 30 days after the close.',
						'Results are shown from the first batch, except the day before and the day of the election, as the law requires. This is not an opinion poll: the voters are not representative.'
					]
				},
				{
					id: 'blanc',
					title: '4. The blank vote counts',
					body: [
						'It is a ballot in its own right: “I voted, but none of them convinces me”. It ranks nobody, but it counts among the voters and in every percentage, with its own line.',
						'You can also put it after your candidates: it then means “not the others”. It meets each candidate in a duel, as if it were one: a candidate whom the majority places below the blank vote falls behind it in the ranking.',
						'Withdrawing your vote is something else: the ballot no longer counts. In the log, a withdrawal looks like any ballot.'
					]
				},
				{
					id: 'journal',
					title: '5. Checking, without trusting us',
					body: [
						'Registrations, encrypted ballots and totals go into a public log, a Merkle tree like those that watch the web’s certificates. Our server signs its state every 20 seconds; each checkpoint commits to everything before it, and its fingerprint is timestamped in Bitcoin: impossible afterwards to pass off another log as that date’s.',
						'Your browser keeps the checkpoints it saw and checks that each new one extends the old without erasing anything, then that your ballot is in the log, as sent.',
						'Anyone can download everything and recount with the open-source tool “dm vote verify”: the signatures, the tree, every passport proof, every ballot proof, the addition of the ballots, the decryption proofs and the results.'
					],
					diagram: {
						actors: [{ label: 'Your browser' }, { label: 'tartuffe.me' }, { label: 'Bitcoin', sub: 'OpenTimestamps' }, { label: 'Anyone' }],
						steps: [
							{ at: 1, note: 'Every entry becomes a leaf of the Merkle tree.', span: 2 },
							{ from: 1, to: 2, label: 'fingerprint of the checkpoint, every 20 s' },
							{ at: 0, note: 'Checks the log only grew and holds my ballot, encrypted, intact.', span: 2 },
							{ from: 3, to: 1, label: 'downloads the whole log' },
							{ at: 3, note: 'dm vote verify: replays registrations, proofs, additions and decryptions.', span: 2 }
						],
						caption: 'Changing, erasing or forgetting a ballot would show.'
					}
				},
				{
					id: 'appareils',
					title: '6. Several devices, a lost key',
					body: [
						'Your key comes from Face ID or your fingerprint, which your phone brings to your other devices; or from 12 words, to write down before the first ballot. With either, you change your ballot from any device, until the close.',
						'Lost everything, key and copies? Your last ballot stays counted, as it is. A passport gives only one key, and nobody can link a ballot to its key: so we can neither cancel it nor give you a second key. A passport never counts for two votes.'
					]
				}
			],
			getTitle: 'What we receive',
			get: [
				'“Adult, French, valid passport: yes”, proved by ZKPassport.',
				'The passport’s identifier for this site, and your key’s public fingerprint. The key itself stays on your devices.',
				'Encrypted ballots and their proofs, read from the relays.',
				'The sum of the ballots, decrypted at every update.'
			],
			neverTitle: 'What we never see',
			never: [
				'Your name, your photo, your date of birth, the passport number.',
				'Your browser’s IP address: it never talks to us, neither to register, nor to vote, nor to check.',
				'Which ballot is yours: a ballot carries neither your passport, nor your key, nor your account.',
				'Your key in clear, your 12 words, your face, your fingerprint.'
			],
			levelsTitle: 'Your level of privacy',
			levelsLead: 'The vote is anonymous for everyone. To go further, it is your choice.',
			levels: [
				{
					name: 'By default',
					how: 'Nothing to do.',
					lines: [
						'Nobody can read your ballot, except the election key, which does not know whose it is.',
						'Our server never sees your IP address: what you send goes through the relays, what you read comes from a public mirror.',
						'The relays see your browser’s IP address, as any site visited does, and an encrypted ballot they cannot read.',
						'The State, which knows the chips of its passports, could tell that yours registered. Never whom it votes for.'
					]
				},
				{
					name: 'With Tor',
					how: 'Open tartuffe.me in Tor Browser, at the “Standard” security level (the other two block the proof computation).',
					lines: [
						'The relays and the mirror see only a Tor network address.',
						'Your internet provider does not see that you vote.',
						'No Face ID or fingerprint in Tor Browser: write down the 12 words, to enter at each visit. Count a few more seconds for the proof.'
					]
				}
			],
			knownTitle: 'At every level',
			known: [
				'A voter can prove their vote to someone by revealing the secret that encrypted it: vote buying is not prevented. Whoever has your key or your 12 words votes in your place.',
				'The election key, with us, could decrypt a single ballot, without knowing whose it is; we publish only the sums, with their proofs.',
				'We serve this page’s code. It is open source, and the log can be checked by others than us.',
				'A single server reads the relays and writes the log. The log, signed and timestamped in Bitcoin, makes any cheating visible.',
				'You need an NFC smartphone and a chip passport. The identity card is not accepted for now: with both, one person would have two votes.',
				'Someone with a full copy of your passport’s chip could register it before you: you would then find your passport already taken.',
				'The ZKPassport request goes through their relay, end-to-end encrypted. ZKPassport is open source, audited internally, not yet by a third party.'
			],
			eraseTitle: 'Withdrawing your vote, erasing your key',
			erase: [
				'Your vote is tied to no account: withdraw it on the vote page, with your key. In the log, a withdrawal looks like a ballot.',
				'“Remove the key from this device”, in the “My key” menu of the vote page, erases it from this browser; Face ID or fingerprint (“Find my key”), your 12 words or your other device bring it back.'
			],
			techTitle: 'For the curious',
			tech: [
				'Ballot: exponential ElGamal on the Grumpkin curve, 10 ciphertexts for 66 values (first choices, ranked candidates, duels), an UltraHonk proof of a Noir circuit (15,000 constraints, no ceremony of ours: Aztec’s universal setup).',
				'Voter list: a Poseidon2 Merkle tree of depth 20; the browser updates its path from public files, never asking for its own leaf.',
				'Totals: Chaum-Pedersen proofs for each component; ranking by the Schulze method on the margins.',
				'Log: a Merkle tree as in RFC 6962; checkpoints as Ed25519 “signed notes” (C2SP format), timestamped by OpenTimestamps.',
				'Recount yourself: dm vote verify presidentielle-2027 --api https://tartuffe.me. The full specification: docs/vote/SPEC.md.'
			],
			links: [
				['ZKPassport’s code (Apache-2.0)', 'https://github.com/zkpassport'],
				['Noir', 'https://noir-lang.org'],
				['OpenTimestamps', 'https://opentimestamps.org']
			],
			cta: 'Go and vote'
		}
	};
	let c = $derived(content[currentLang]);
</script>

<svelte:head>
	<title>{c.kicker} — tartuffe.me</title>
</svelte:head>

<article class="how">
	<header class="hero">
		<span class="kicker">{c.kicker}</span>
		<h1>{c.title}</h1>
		<p class="lead">{c.lead}</p>
	</header>

	<section class="brief" aria-labelledby="brief-title">
		<h2 id="brief-title">{c.briefTitle}</h2>
		<ol>{#each c.brief as line}<li>{line}</li>{/each}</ol>
	</section>

	<nav class="toc" aria-label={c.toc}>
		{#each c.parts as p (p.id)}<a href="#{p.id}">{p.title}</a>{/each}
		<a href="#niveaux">{c.levelsTitle}</a>
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

	<section class="part" id="niveaux">
		<h2>{c.levelsTitle}</h2>
		<p>{c.levelsLead}</p>
		<details class="more">
		<summary>{c.levelsMore}</summary>
		<div class="levels">
			{#each c.levels as level, i (i)}
				<div class="box level">
					<h3>
						<svg class="shield" viewBox="0 0 24 24" aria-hidden="true">
							<path d="M12 3l7.5 3v5.5c0 4.6-3.2 8.3-7.5 9.5-4.3-1.2-7.5-4.9-7.5-9.5V6z" />
							{#each Array(i + 1) as _, k (k)}<path class="bar" d="M8.5 {14 - k * 3.5}h7" />{/each}
						</svg>
						{level.name}
					</h3>
					<p class="how">{level.how}</p>
					<ul>{#each level.lines as line}<li>{line}</li>{/each}</ul>
				</div>
			{/each}
		</div>
		<h3 class="known-title">{c.knownTitle}</h3>
		<ul class="known">{#each c.known as line}<li>{line}</li>{/each}</ul>
		</details>
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
	/* The short version: five lines before anything else. */
	.brief {
		margin: 0 0 1.75rem;
		padding: 1.1rem 1.3rem;
		border: 1px solid var(--event);
		border-radius: var(--radius-lg);
		background: var(--bg-card);
	}
	.brief h2 {
		margin: 0 0 0.6rem;
		font-size: 0.78rem;
		font-weight: 700;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--event);
	}
	.brief ol {
		margin: 0;
		padding-left: 1.3rem;
		display: grid;
		gap: 0.4rem;
		line-height: 1.5;
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
	.levels {
		display: grid;
		grid-template-columns: 1fr 1fr;
		gap: 1rem;
		margin: 1rem 0 1.5rem;
	}
	.level {
		border-top: 2px solid var(--event);
	}
	h3 {
		font-family: var(--font-serif);
		font-size: 1.2rem;
		font-weight: 600;
		margin: 0 0 0.5rem;
	}
	.level h3 {
		display: flex;
		align-items: center;
		gap: 0.5rem;
	}
	.shield {
		width: 1.6rem;
		height: 1.6rem;
		flex-shrink: 0;
		fill: color-mix(in oklch, var(--event) 18%, transparent);
		stroke: var(--event);
		stroke-width: 1.6;
		stroke-linejoin: round;
	}
	.shield .bar {
		stroke-width: 2;
		stroke-linecap: round;
	}
	.how {
		color: var(--text-primary);
		font-weight: 500;
	}
	.known li::marker {
		color: var(--text-muted);
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
		.split,
		.levels {
			grid-template-columns: 1fr;
		}
		h2 {
			font-size: 1.45rem;
		}
	}
</style>
