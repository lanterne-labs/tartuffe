// Site-wide language toggle (FR ↔ EN). One global state, persisted to
// localStorage so the user's preference survives reloads and navigation.
// Use t(key) for static UI strings; use pickLang(native, en, lang) for dynamic
// LLM-translated content (falls back to native when EN is missing).

import { writable, get, type Writable } from 'svelte/store';

export type Lang = 'fr' | 'en';

function initialLang(): Lang {
	if (typeof window === 'undefined') return 'fr';
	const v = window.localStorage.getItem('lang');
	return v === 'en' ? 'en' : 'fr';
}

export const lang: Writable<Lang> = writable(initialLang());

if (typeof window !== 'undefined') {
	lang.subscribe((v) => {
		try {
			window.localStorage.setItem('lang', v);
		} catch {}
	});
}

export function setLang(v: Lang) {
	lang.set(v);
}

export function pickLang(native: string | null | undefined, en: string | null | undefined, currentLang: Lang): string {
	if (currentLang === 'en' && en && en.trim() !== '') return en;
	return native ?? '';
}

// Static UI translations. Keep this small and explicit — every key is a string
// that appears verbatim in a Svelte template. No interpolation framework; if a
// string needs a count, build it in the component (e.g. `${n} ${t('facts', n)}`).
const dict: Record<string, { fr: string; en: string }> = {
	// Nav
	'nav.dashboard': { fr: 'Tableau de bord', en: 'Dashboard' },

	// Home / dashboard
	'home.search_placeholder': { fr: 'Rechercher par nom…', en: 'Search by name…' },
	'home.filter.all': { fr: 'Tous', en: 'All' },
	'home.filter.people': { fr: 'Personnes', en: 'People' },
	'home.filter.parties': { fr: 'Partis', en: 'Parties' },
	'home.most_honest': { fr: 'Les plus honnêtes', en: 'Most Honest' },
	'home.least_honest': { fr: 'Les moins honnêtes', en: 'Least Honest' },
	'home.all_entities': { fr: 'Personnalités et partis', en: 'People and parties' },
	'home.no_data': { fr: 'Aucune donnée.', en: 'No data yet.' },
	'home.load_more': { fr: 'Afficher plus', en: 'Load more' },
	'home.retry': { fr: 'Réessayer', en: 'Retry' },
	'home.party_badge': { fr: 'Parti', en: 'Party' },

	// Person page
	'person.score': { fr: 'Score', en: 'Score' },
	'person.own_score': { fr: 'Son score', en: 'Own score' },
	'person.members_score': { fr: 'Score de ses membres', en: 'Its members' },
	'person.aggregate': { fr: 'Score global', en: 'Overall' },
	'person.party_badge': { fr: 'Parti', en: 'Party' },
	'person.stances': { fr: 'Positions', en: 'Stances' },
	'person.no_stances': { fr: 'Aucune position pour l’instant.', en: 'No stances yet.' },
	'person.sort_by': { fr: 'Trier par', en: 'Sort by' },
	'person.sort.worst': { fr: 'Les pires d’abord', en: 'Worst first' },
	'person.sort.recent': { fr: 'Les plus récentes', en: 'Most recent' },
	'person.sort.most_facts': { fr: 'Les plus documentées', en: 'Most facts' },
	'person.no_contradictions_chip': { fr: 'Aucune contradiction pour l’instant', en: 'No contradictions yet' },
	'person.updated': { fr: 'Mis à jour', en: 'Updated' },
	'person.history': { fr: 'Évolution', en: 'Over time' },
	'person.history.score': { fr: 'Score depuis le début', en: 'Score (all time)' },
	'person.history.own_score': { fr: 'Score personnel depuis le début', en: 'Own score (all time)' },
	'person.history.recent': { fr: 'Score sur les 12 derniers mois', en: 'Score (last 12 months)' },
	'person.history.facts': { fr: 'Faits par semaine', en: 'Facts per week' },
	'person.history.facts_total': { fr: 'faits au total', en: 'facts in total' },
	'person.history.empty': { fr: 'Pas encore d’historique.', en: 'No history yet.' },
	'person.history.error': { fr: 'Historique indisponible', en: 'History unavailable' },
	'person.history.range': { fr: 'Période', en: 'Time range' },
	'person.history.range.3m': { fr: '3 mois', en: '3M' },
	'person.history.range.1y': { fr: '1 an', en: '1Y' },
	'person.history.range.all': { fr: 'Tout', en: 'All' },
	'person.history.axis_note': { fr: 'Comptés par semaine, à la date des déclarations.', en: 'Weekly, by statement date.' },
	'person.tab.contradictions': { fr: 'Contradictions', en: 'Contradictions' },
	'person.tab.facts': { fr: 'Faits', en: 'Facts' },
	'person.tab.no_contradictions': { fr: 'Aucune contradiction relevée.', en: 'No contradictions here.' },
	'person.tab.no_facts': { fr: 'Aucun fait relevé.', en: 'No facts here.' },
	'person.loading': { fr: 'Chargement…', en: 'Loading…' },
	'person.source.original': { fr: 'Source', en: 'Source' },
	'person.source.archive': { fr: 'Archive', en: 'Archive' },
	'fact.reported_n_times': { fr: 'Cité {n} fois', en: 'Reported {n} times' },
	'person.show_n_more': { fr: 'Afficher {n} de plus', en: 'Show {n} more' },
	'person.show_all_n': { fr: 'Tout afficher ({n})', en: 'Show all {n}' },
	'person.retry': { fr: 'Réessayer', en: 'Retry' },

	// Contradiction pair (component)
	'contradiction.view_in_facts': { fr: 'Voir dans les faits', en: 'View in Facts' },
	'contradiction.facts_in_tension': { fr: '{n} faits qui se contredisent', en: '{n} facts in tension' },
	'contradiction.base_fact': { fr: 'Affirmation de départ', en: 'Base claim' },

	// Pre-poll (election page)
	'poll.kicker': { fr: 'Consultation citoyenne', en: 'Citizen pre-poll' },
	'poll.disclaimer': { fr: 'Cette consultation des utilisateurs de tartuffe.me n’est pas représentative de la population : ce n’est pas un sondage.', en: 'A consultation of tartuffe.me users, not representative of the population. This is not an opinion poll.' },
	'poll.voters': { fr: '{n} votants', en: '{n} voters' },
	'poll.closes': { fr: 'Vote ouvert jusqu’au {d}', en: 'Voting open until {d}' },
	'poll.closed': { fr: 'Le vote est clos.', en: 'Voting is closed.' },
	'poll.hidden.blackout': { fr: 'Les résultats sont masqués : la loi interdit de les publier la veille et le jour du scrutin.', en: 'Results hidden: the law forbids publishing them the day before and the day of the vote.' },
	'poll.how.title': { fr: 'Comment lire cette page', en: 'How to read this page' },
	'vote.slot.choice': { fr: '{rank} choix', en: '{rank} choice' },
	'vote.blank.help': { fr: 'Le vote blanc peut occuper n’importe quelle case. Seul, c’est un vote blanc classique. Placé après vos candidats, il veut dire « les autres, non » : il est alors opposé à chaque candidat, comme un candidat.', en: 'The blank vote can take any slot. Alone, it is a blank vote. After your candidates, it means: the others, no. It is counted against each candidate, like a candidate.' },
	'vote.title': { fr: 'Mon vote', en: 'My vote' },
	'vote.hint': { fr: 'Classez jusqu’à {n} candidats, par ordre de préférence.', en: 'Rank up to {n} candidates, in order of preference.' },
	'vote.closed_hint': { fr: 'Le vote est clos.', en: 'Voting is closed.' },
	'vote.remove_named': { fr: 'Retirer mon vote pour {name}', en: 'Remove my vote for {name}' },
	'vote.withdraw_all': { fr: 'Retirer mon vote', en: 'Remove my vote' },
	'vote.withdraw.title': { fr: 'Retirer votre vote ?', en: 'Remove your vote?' },
	'vote.withdraw.text': { fr: 'Votre classement sera retiré. Vous pourrez revoter.', en: 'Your ranking will be removed. You can vote again.' },
	'vote.cancel': { fr: 'Annuler', en: 'Cancel' },
	'profile.title': { fr: 'Mon profil', en: 'My profile' },
	'profile.sign_in_text': { fr: 'Facultatif : un compte sert à signaler une erreur et à soutenir une contradiction (+1). Il n’est pas nécessaire pour voter.', en: 'Optional: to report errors and give +1s. Not needed to vote.' },
	'profile.account': { fr: 'Mon compte', en: 'My account' },
	'profile.google': { fr: 'Vous êtes connecté avec Google, qui ne nous transmet ni e-mail ni nom.', en: 'Signed in with Google, no e-mail or name shared.' },
	'profile.test': { fr: 'Ce compte de test n’est pas vérifié.', en: 'Test account, not verified.' },
	'profile.provider_other': { fr: 'Connecté avec {p}', en: 'Signed in with {p}' },
	'profile.votes': { fr: 'Mes votes', en: 'My votes' },
	'profile.no_events': { fr: 'Aucun vote ouvert.', en: 'No vote open.' },
	'profile.open_until': { fr: 'Ouvert jusqu’au {d}', en: 'Open until {d}' },
	'profile.open': { fr: 'Ouvert', en: 'Open' },
	'profile.closed': { fr: 'Clos : les votes sont définitifs', en: 'Closed, votes final' },
	'profile.go_vote': { fr: 'Voter', en: 'Vote' },
	'profile.go_results': { fr: 'Voir les résultats', en: 'See the results' },
	'profile.danger': { fr: 'Supprimer mon compte', en: 'Delete my account' },
	'profile.danger_text': { fr: 'Supprime le compte, les signalements et les sessions. Votre vote, anonyme, n’y est pas rattaché : retirez-le d’abord si vous le souhaitez.', en: 'Deletes the account, the reports and the sessions. The vote is anonymous and not tied to it: withdraw it first if needed.' },
	'profile.delete': { fr: 'Supprimer mon compte', en: 'Delete my account' },
	'profile.delete.title': { fr: 'Supprimer votre compte ?', en: 'Delete your account?' },
	'profile.delete.text': { fr: 'La suppression est définitive. Votre clé de vote reste sur cet appareil.', en: 'This cannot be undone. Your voting key stays on this device.' },
	'profile.deleted': { fr: 'Compte supprimé.', en: 'Account deleted.' },
	'profile.error': { fr: 'Impossible de charger vos votes.', en: 'Could not load your votes.' },
	'nav.profile': { fr: 'Mon profil', en: 'My profile' },

	// Home promotion of the featured pre-poll
	'home.poll.title': { fr: 'Votre candidat tient-il parole ?', en: 'Does your candidate tell the truth?' },
	'home.poll.lead': { fr: 'Classez vos candidats et découvrez leur score d’honnêteté.', en: 'Rank your candidates and see their honesty score alongside.' },
	'home.poll.cta': { fr: 'Voter et comparer', en: 'Vote and compare' },
	'home.poll.candidates': { fr: '{n} candidats', en: '{n} candidates' },

	// ZKPassport sign-in
	'zk.title': { fr: 'Passeport ou carte d’identité', en: 'Passport or ID card' },
	'zk.badge': { fr: '1 personne = 1 voix', en: '1 person = 1 vote' },
	'zk.why': { fr: 'Le scan garantit que chacun ne vote qu’une fois. Nous ne voyons ni votre nom ni votre photo.', en: 'So each person votes once. We never see your name or photo.' },
	'zk.start': { fr: 'Scanner mon document', en: 'Verify with my document' },
	'zk.open_app': { fr: 'Ouvrir l’app ZKPassport', en: 'Open the ZKPassport app' },
	'zk.phone_hint': { fr: 'Revenez ici une fois la preuve envoyée.', en: 'Come back here once the proof is sent.' },
	'zk.qr_alt': { fr: 'QR code de la demande ZKPassport', en: 'QR code of the ZKPassport request' },
	'zk.scan_hint': { fr: 'Scannez avec l’app ZKPassport.', en: 'Scan with the ZKPassport app.' },
	'zk.get_app': { fr: 'Pas encore l’app ?', en: 'No app yet?' },
	'zk.cancel': { fr: 'Annuler', en: 'Cancel' },
	'zk.flow.qr': { fr: 'QR code', en: 'QR code' },
	'zk.flow.app': { fr: 'Dans l’app', en: 'In the app' },
	'zk.flow.proof': { fr: 'Preuve', en: 'Proof' },
	'zk.flow.check': { fr: 'Vérifiée', en: 'Checked' },
	'zk.rejected': { fr: 'Vous avez refusé dans l’app.', en: 'Declined in the app.' },
	'zk.error': { fr: 'La vérification a échoué.', en: 'Verification failed.' },
	'zk.retry': { fr: 'Recommencer', en: 'Start again' },
	'zk.purpose': { fr: 'Prouver que vous êtes majeur et de nationalité française, sans révéler votre identité, pour voter sur tartuffe.me.', en: 'Prove that you are an adult French national, without revealing who you are, to vote on tartuffe.me.' },
	'zk.anim.alt': { fr: 'Animation : le téléphone lit votre document, n’en garde que « Français » et « 18 ans et + », les scelle dans une preuve et n’envoie que la preuve.', en: 'Animation: the phone reads your document, keeps only “French” and “18+”, seals them in a proof and sends only the proof.' },
	'zk.anim.doc': { fr: 'Votre document', en: 'Your document' },
	'zk.anim.stays': { fr: 'reste sur le téléphone', en: 'stays on the phone' },
	'zk.anim.french': { fr: 'Français', en: 'French' },
	'zk.anim.adult': { fr: '18 ans et +', en: '18+' },
	'zk.anim.proof': { fr: 'Preuve', en: 'Proof' },
	'zk.anim.sealed': { fr: 'Preuve scellée', en: 'Sealed proof' },
	'zk.anim.server': { fr: 'Nos serveurs', en: 'Our servers' },
	'zk.anim.never': { fr: 'Nous ne lisons jamais votre document.', en: 'We never read your document.' },
	'zk.anim.only': { fr: 'Nous recevons seulement :', en: 'All we receive:' },
	'zk.anim.and': { fr: 'et', en: 'and' },
	'zk.anim.note': { fr: 'Le téléphone envoie aussi un identifiant anonyme, propre à ce site, pour qu’un document ne vote qu’une fois. Nom, photo, date de naissance et numéro restent sur le téléphone.', en: 'And an anonymous identifier, unique to this site, so one document votes once. Name, photo, birth date and number stay on the phone.' },
	'zk.dev_mode': { fr: 'Mode test : les documents fictifs sont acceptés.', en: 'Test mode: mock documents accepted.' },

	// Sign-in
	'auth.google.button': { fr: 'Continuer avec Google', en: 'Continue with Google' },
	'auth.google.private': { fr: 'Google ne nous transmet qu’un identifiant anonyme : ni e-mail, ni nom, ni photo.', en: 'Google only gives us an anonymous identifier: no e-mail, name or photo.' },
	'auth.privacy_link': { fr: 'Confidentialité', en: 'Privacy' },
	'auth.google.failed': { fr: 'La connexion avec Google a échoué.', en: 'Google sign-in failed.' },
	'auth.google.back': { fr: 'Retour au site', en: 'Back to the site' },
	'auth.google.signing_in': { fr: 'Connexion…', en: 'Signing in…' },
	'auth.name.title': { fr: 'Choisissez un pseudo', en: 'Choose a pseudonym' },
	'auth.name.hint': { fr: 'Vous seul le voyez.', en: 'Only you see it.' },
	'auth.name.placeholder': { fr: 'Votre pseudo', en: 'Your pseudonym' },
	'auth.name.submit': { fr: 'Enregistrer', en: 'Save' },
	'profile.rename': { fr: 'Modifier', en: 'Edit' },
	// The lab and its temporary events
	'nav.lab': { fr: 'Le labo', en: 'The lab' },
	'footer.lab': { fr: 'tartuffe.me, un laboratoire d’outils pour une démocratie plus transparente, où les élus rendent des comptes.', en: 'tartuffe.me, an R&D lab for more transparent and accountable democracies.' },
	'event.label': { fr: 'Expérience temporaire du labo', en: 'Temporary experiment of the lab' },
	'event.tag': { fr: 'Expérience temporaire', en: 'Temporary experiment' },
	'event.tag_short': { fr: 'Événement', en: 'Event' },
	'event.pitch': { fr: 'Votez en ligne avec votre passeport. Personne, ni nous ni l’État, ne sait qui a voté quoi.', en: 'Vote online with your passport. Nobody, neither we nor the State, knows who voted what.' },
	'event.ends': { fr: 'Fin le {d} (J-{n})', en: 'Ends {d} ({n} days left)' },
	'event.cta': { fr: 'Voter', en: 'Vote' },
	'event.strip': { fr: 'Une expérience du labo tartuffe.me : le vote en ligne vraiment secret.', en: 'An experiment of the tartuffe.me lab: truly secret online voting.' },
	'event.strip.ends': { fr: 'Une expérience du labo tartuffe.me, jusqu’au {d}.', en: 'An experiment of the tartuffe.me lab, until {d}.' },
	'event.menu.label': { fr: 'Pages de l’événement', en: 'Event pages' },
	'event.menu.intro': { fr: 'Présentation', en: 'Overview' },
	'event.menu.vote': { fr: 'Voter', en: 'Vote' },
	'event.menu.how': { fr: 'Comment ça marche', en: 'How it works' },
	'event.menu.intro_sub': { fr: 'L’expérience en une minute', en: 'The experiment in a minute' },
	'event.menu.vote_sub': { fr: 'Classer jusqu’à 3 candidats', en: 'Rank up to 3 candidates' },
	'event.menu.how_sub': { fr: 'Anonymat, vérifications, limites', en: 'Anonymity, checks, limits' },
	'event.strip.why': { fr: 'Pourquoi', en: 'Why' },
	'vote.error.full': { fr: 'Déjà {n} candidats : retirez-en un.', en: 'Already {n} candidates: remove one.' },
	// The voting key (lib/voter.ts)
	'poll.list.ranking': { fr: 'Avec le classement', en: 'With the ranking' },
	'poll.condorcet': { fr: 'Bat chacun des autres en duel', en: 'Preferred to each other candidate head to head' },
	'poll.first_ranked': { fr: '1er choix {f} · classé par {r}', en: '1st choice {f} · ranked by {r}' },
	'key.back': { fr: 'Retour', en: 'Back' },
	'vote.blank.button': { fr: 'Voter blanc', en: 'Vote blank' },
	'vote.blank.name': { fr: 'Vote blanc', en: 'Blank vote' },
	'profile.key.title': { fr: 'Ma clé de vote', en: 'My voting key' },
	'profile.key.none': { fr: 'Cet appareil n’a pas de clé de vote. Obtenez-la sur la page du vote, avec votre passeport ; ou retrouvez-la avec vos 12 mots. Aucun compte n’est nécessaire.', en: 'No voting key on this device. Get one on the vote page, with your passport; or recover it with your 12 words. No account needed.' },
	'profile.key.ready': { fr: 'Votre clé de vote est sur cet appareil.', en: 'Your voting key is on this device.' },
	'profile.key.story': { fr: 'Votre passeport a servi une fois. La clé reste sur vos appareils : c’est elle qui vote, et qui vous permet de changer votre bulletin jusqu’à la clôture.', en: 'Your passport served once. The key stays on your devices: it is what votes, and what lets you change your ballot until the close.' },
	'profile.key.unprotected': { fr: 'Votre clé n’a aucune copie : si cet appareil est perdu, votre dernier bulletin reste compté mais vous ne pourrez plus le changer.', en: 'No copy of your key: lose this device and your last ballot stays counted, but you can no longer change it.' },
	'profile.key.lost.title': { fr: 'Et si je perds tout ?', en: 'What if I lose everything?' },
	'profile.key.lost.text': { fr: 'Un passeport donne une seule clé, et personne ne peut relier vos bulletins à votre clé : nous ne pouvons donc pas en annuler un. Sans la clé et sans ses copies, votre dernier bulletin reste compté tel quel.', en: 'A passport gives one key, and nobody can link your ballots to your key: so none can be cancelled. Without the key and its copies, your last ballot stays counted as it is.' },
	'profile.key.forget': { fr: 'Retirer la clé de cet appareil', en: 'Remove the key from this device' },
	'profile.key.forget.title': { fr: 'Retirer la clé de cet appareil ?', en: 'Remove the key from this device?' },
	'profile.key.forget.text': { fr: 'Votre vote reste compté. Vous pourrez récupérer la clé avec votre clé d’accès, un autre appareil ou vos 12 mots.', en: 'Your vote still counts. You can bring the key back with your passkey, another device or your 12 words.' },
	'profile.key.forget.text_unprotected': { fr: 'Attention : aucune copie de votre clé. Votre vote reste compté, mais vous ne pourrez plus le changer avant la prochaine période de 30 jours.', en: 'Warning: no copy of your key. Your vote still counts, but you cannot change it before the next 30-day period.' },
	'profile.ballot.none': { fr: 'Aucun vote depuis cet appareil.', en: 'No vote from this device yet.' },
	'profile.ballot.blank': { fr: 'Vous votez blanc.', en: 'You vote blank.' },
	'profile.ballot.one': { fr: 'Vous classez 1 candidat.', en: 'You rank 1 candidate.' },
	'profile.ballot.many': { fr: 'Vous classez {n} candidats.', en: 'You rank {n} candidates.' },
	'profile.ballot.no_key': { fr: 'Il vous faut une clé de vote.', en: 'You need a voting key.' },
	'profile.account.apart': { fr: 'Il sert aux signalements et aux +1, et n’est jamais relié à votre vote.', en: 'For reports and +1s. Never tied to your vote.' },
	'auth.title': { fr: 'Se connecter', en: 'Sign in' },
	'auth.signed_in_as': { fr: 'Connecté : {name}', en: 'Signed in: {name}' },
	'auth.sign_out': { fr: 'Se déconnecter', en: 'Sign out' },
	'auth.close': { fr: 'Fermer', en: 'Close' },
	'auth.none': { fr: 'Le vote n’est pas encore ouvert.', en: 'Voting is not open yet.' },
	'auth.noop.title': { fr: 'Compte de test', en: 'Test account' },
	'auth.noop.hint': { fr: 'Réservé aux tests : un pseudonyme suffit, sans vérification.', en: 'Testing only: a pseudonym, with no check.' },
	'auth.noop.placeholder': { fr: 'Pseudonyme', en: 'Pseudonym' },
	'auth.noop.submit': { fr: 'Continuer', en: 'Continue' },
	'auth.anonymous': { fr: 'votant anonyme', en: 'anonymous voter' },

	// Counts (singular/plural — caller picks the form, but we expose both)
	'count.fact': { fr: 'fait', en: 'fact' },
	'count.facts': { fr: 'faits', en: 'facts' },
	'count.stance': { fr: 'position', en: 'stance' },
	'count.stances': { fr: 'positions', en: 'stances' },
	'count.contradiction': { fr: 'contradiction', en: 'contradiction' },
	'count.contradictions': { fr: 'contradictions', en: 'contradictions' },
	'count.over': { fr: 'sur', en: 'over' },

	// Nav
	'nav.feed': { fr: 'Le fil', en: 'Feed' },
	'nav.menu.project': { fr: 'Le projet', en: 'The project' },
	'nav.menu.project_sub': { fr: 'Ce que mesure tartuffe.me', en: 'What tartuffe.me measures' },
	'nav.menu.lab': { fr: 'Le labo', en: 'The lab' },
	'nav.menu.lab_sub': { fr: 'Nos expériences temporaires', en: 'Our temporary experiments' },
	'nav.menu.privacy': { fr: 'Confidentialité', en: 'Privacy' },
	'nav.menu.privacy_sub': { fr: 'Ce que nous gardons, et pourquoi', en: 'What we keep, and why' },
	'nav.about': { fr: 'À propos', en: 'About' },
	'nav.privacy': { fr: 'Confidentialité', en: 'Privacy' },

	// Share
	'share.title': { fr: 'Partager', en: 'Share' },
	'share.x': { fr: 'Publier sur X', en: 'Post on X' },
	'share.copy': { fr: 'Copier le lien', en: 'Copy link' },
	'share.copied': { fr: 'Lien copié', en: 'Link copied' },
	'share.more': { fr: 'Partager via…', en: 'Share via…' },
	'share.text.person': { fr: '{name} : {score}/100 de cohérence sur tartuffe.me', en: '{name}: {score}/100 for consistency on tartuffe.me' },
	'share.text.stance': { fr: 'Les déclarations et les votes de {name} sur « {topic} »', en: 'What {name} says and votes on “{topic}”' },
	'share.text.contradiction': { fr: '{name} : « {a} » puis « {b} »', en: '{name}: “{a}”, then “{b}”' },
	'share.text.fact': { fr: '{name} : « {fact} »', en: '{name}: “{fact}”' },

	// Score explanation
	'score.help': { fr: 'Comment ce score est calculé', en: 'How this score is computed' },
	'score.title': { fr: 'Le score de {name}', en: 'The score of {name}' },
	'score.title.stance': { fr: '« {topic} »', en: '“{topic}”' },
	'score.error': { fr: 'Le détail du score est indisponible. Réessayez dans un instant.', en: 'Score details unavailable. Try again in a moment.' },
	'score.facts': { fr: 'Faits', en: 'Facts' },
	'score.contradictions': { fr: 'Contradictions', en: 'Contradictions' },
	'score.members': { fr: 'Membres', en: 'Members' },
	'score.what': { fr: 'Le score mesure la cohérence entre les paroles et les actes, pas la vérité.', en: 'The score measures how words match deeds. It is not the truth.' },
	'score.more': { fr: 'La méthode en détail', en: 'The method in detail' },

	// Reports
	'report.title.fact': { fr: 'Signaler un fait erroné', en: 'Report a wrong fact' },
	'report.title.contradiction': { fr: 'Signaler une contradiction erronée', en: 'Report a wrong contradiction' },
	'report.done_short': { fr: 'Signalé', en: 'Reported' },
	'report.why.fact': { fr: 'Qu’est-ce qui est faux ? (attribution, citation, résumé, source…)', en: 'What is wrong? (attribution, quote, summary, source…)' },
	'report.why.contradiction': { fr: 'Pourquoi n’est-ce pas une contradiction ? (contextes différents, fait erroné…)', en: 'Why is it not a contradiction? (different contexts, a wrong fact…)' },
	'report.placeholder.fact': { fr: 'Par exemple : dans la vidéo, elle cite un adversaire ; ce n’est pas sa position.', en: 'E.g. in the video she quotes an opponent; it is not her position.' },
	'report.placeholder.contradiction': { fr: 'Par exemple : le premier vote porte sur la loi de 2019, le second sur un autre texte.', en: 'E.g. the first vote is on the 2019 law, the second on another text.' },
	'report.hint': { fr: 'Une IA relit votre signalement avec les sources : apportez un élément concret.', en: 'An AI checks your report against the sources. Give concrete evidence.' },
	'report.submit': { fr: 'Envoyer', en: 'Send' },
	'report.sending': { fr: 'Envoi…', en: 'Sending…' },
	'report.sent': { fr: 'Merci, votre signalement est enregistré.', en: 'Thanks, report recorded.' },
	'report.sent_next': { fr: 'Une IA le relit avec les sources dans les minutes qui viennent. Si vous avez raison, l’élément est retiré et la correction publiée.', en: 'An AI checks it against the sources within minutes. If you are right, the item is removed and the correction published.' },
	'report.see_profile': { fr: 'Suivre mes signalements', en: 'Follow my reports' },
	'report.error.too_many_reports': { fr: 'Vous avez déjà 20 signalements en attente.', en: 'You already have 20 reports waiting.' },
	'report.error.not_found': { fr: 'Cet élément n’existe plus : il a sans doute déjà été corrigé.', en: 'This item no longer exists: it may have been corrected already.' },
	'report.error.invalid_report': { fr: 'Expliquez ce qui est faux (10 caractères minimum).', en: 'Explain what is wrong (10 characters minimum).' },
	'report.error.error': { fr: 'L’envoi a échoué. Réessayez.', en: 'Sending failed. Try again.' },
	'report.status.pending': { fr: 'En cours d’examen', en: 'Being reviewed' },
	'report.status.accepted': { fr: 'Corrigé', en: 'Corrected' },
	'report.status.rejected': { fr: 'Maintenu', en: 'Kept' },
	'report.status.ambiguous': { fr: 'En attente de relecture humaine', en: 'Waiting for a human review' },
	'report.status.stale': { fr: 'Déjà retiré', en: 'Already removed' },

	// Profile: reports
	'profile.reports': { fr: 'Mes signalements', en: 'My reports' },
	'profile.reports.none': { fr: 'Aucun signalement. Un fait ou une contradiction vous semble erroné ? Cliquez sur le drapeau à côté.', en: 'No reports. Something looks wrong? Click the flag next to it.' },
	'profile.reports.judge': { fr: 'Avis de l’IA :', en: 'The AI’s view:' },

	// Feed
	'feed.title': { fr: 'Le fil', en: 'The feed' },
	'feed.sort.hot': { fr: 'À la une', en: 'Top' },
	'feed.sort.new': { fr: 'Récent', en: 'Latest' },
	'feed.hot_end': { fr: 'C’est tout pour la une.', en: 'That’s all for the top.' },
	'feed.hot_empty': { fr: 'Rien de neuf ces deux dernières semaines.', en: 'Nothing new in the last two weeks.' },
	'feed.see_new': { fr: 'Voir tout, du plus récent au plus ancien', en: 'See everything, latest first' },
	'like.do': { fr: '+1 : mettre en avant', en: '+1: worth seeing' },
	'like.undo': { fr: 'Retirer mon +1', en: 'Take back my +1' },
	'feed.all': { fr: 'Tout', en: 'All' },
	'feed.contradictions': { fr: 'Contradictions', en: 'Contradictions' },
	'feed.facts': { fr: 'Faits', en: 'Facts' },
	'feed.effect': { fr: 'Effet sur le score de {name} aujourd’hui', en: 'Effect on {name}’s score today' },
	'feed.tag.contradiction': { fr: 'Se contredit', en: 'Contradicts' },
	'feed.person': { fr: 'Son fil', en: 'Their feed' },
	'feed.empty': { fr: 'Rien pour l’instant.', en: 'Nothing yet.' },
	'feed.end': { fr: 'Fin du fil.', en: 'End of the feed.' },
	'feed.error': { fr: 'Le fil est indisponible.', en: 'Feed unavailable.' },
	'feed.new': { fr: 'Afficher {n} nouveautés', en: 'Show {n} new items' },
	'feed.filter.open': { fr: 'Filtres', en: 'Filters' },
	'feed.filter.person': { fr: 'Filtrer par personne…', en: 'Filter by person…' },
	'feed.filter.clear_person': { fr: 'Retirer le filtre de personne', en: 'Remove the person filter' },
	'feed.filter.from': { fr: 'Du', en: 'From' },
	'feed.filter.to': { fr: 'au', en: 'to' },
	'feed.filter.reset': { fr: 'Effacer les filtres', en: 'Clear filters' },
	'feed.empty_filtered': { fr: 'Rien ne correspond à ces filtres.', en: 'Nothing for these filters.' },
	'feed.more': { fr: 'Charger la suite', en: 'Load more' },

	// Corrections
	'corrections.title': { fr: 'Corrections', en: 'Corrections' },
	'corrections.intro': { fr: 'Voici les éléments signalés puis retirés, et la raison de leur retrait.', en: 'Reported items we removed, with the reason.' },
	'corrections.kind.fact': { fr: 'Fait retiré', en: 'Fact removed' },
	'corrections.kind.contradiction': { fr: 'Contradiction retirée', en: 'Contradiction removed' },
	'corrections.reason': { fr: 'Raison', en: 'Reason' },
	'corrections.empty': { fr: 'Aucune correction pour l’instant.', en: 'No correction yet.' },
	'corrections.on': { fr: 'corrigé le {d}', en: 'corrected on {d}' },
	// tartuffe-vote/2 (the vote page, lib/vote)
	'v2.counter.ballots': { fr: '{n} bulletins reçus', en: '{n} ballots received' },
	'v2.counter.since': { fr: '{n} depuis la dernière mise à jour', en: '{n} since the last update' },
	'v2.counter.next': { fr: 'prochaine mise à jour dès {k} bulletins modifiés', en: 'next update once {k} ballots have changed' },
	'v2.results.none': { fr: 'Pas encore de résultats : ils apparaissent dès que {k} bulletins ont changé.', en: 'No results yet: they appear once {k} ballots have changed.' },
	'v2.results.updated': { fr: 'Résultats mis à jour {when}', en: 'Results updated {when}' },
	'v2.blackout': { fr: 'Résultats figés pendant la période électorale.', en: 'Results frozen during the electoral period.' },
	'v2.status.proving': { fr: 'Calcul de la preuve…', en: 'Computing the proof…' },
	'v2.status.sending': { fr: 'Envoi…', en: 'Sending…' },
	'v2.status.received': { fr: 'Bulletin reçu', en: 'Ballot received' },
	'v2.status.received.help': { fr: 'Bulletin reçu. Il rejoindra le registre public au prochain point de contrôle, dans moins d’une minute.', en: 'Ballot received. It joins the public log at the next checkpoint, within a minute.' },
	'v2.status.verified': { fr: 'Bulletin dans le registre', en: 'Ballot in the log' },
	'v2.status.missing': { fr: 'Bulletin absent du registre', en: 'Ballot missing from the log' },
	'v2.status.missing.help': { fr: 'Notre serveur a accusé réception de votre bulletin pour un point de contrôle qui ne le contient pas. Votre navigateur a renvoyé le bulletin à deux autres relais et y a publié l’accusé de réception signé : la preuve publique de ce manquement, que tout auditeur vérifie.', en: 'Our server acknowledged your ballot for a checkpoint that does not hold it. Your browser reposted the ballot to two other relays and published the signed acknowledgement there: the public proof of this failure, which any auditor checks.' },
	'v2.status.missing.links': { fr: 'La preuve, publique : ', en: 'The proof, public: ' },
	'v2.status.missing.verify': { fr: 'vérifier', en: 'check it' },
	'v2.status.missing.event': { fr: 'l’événement', en: 'the event' },
	'v2.complaint.title': { fr: 'Une réclamation', en: 'A complaint' },
	'v2.complaint.lead': { fr: 'Un votant publie l’accusé de réception signé par notre serveur pour un bulletin que le registre ne contient pas. Votre navigateur refait la vérification : rien n’est à croire sur parole.', en: 'A voter published the acknowledgement our server signed for a ballot the log does not hold. Your browser redoes the check: nothing to take on trust.' },
	'v2.complaint.loading': { fr: 'Lecture de la réclamation sur les relais…', en: 'Reading the complaint on the relays…' },
	'v2.complaint.notfound': { fr: 'Aucun relais ne sert cet événement.', en: 'No relay serves this event.' },
	'v2.complaint.badack': { fr: 'L’accusé de réception joint n’est pas signé par notre serveur : la réclamation ne prouve rien.', en: 'The acknowledgement inside is not signed by our server: the complaint proves nothing.' },
	'v2.complaint.ack': { fr: 'Accusé de réception signé par notre serveur : bulletin {pi}, point de contrôle {n}.', en: 'Acknowledgement signed by our server: ballot {pi}, checkpoint {n}.' },
	'v2.complaint.nocp': { fr: 'Le point de contrôle {n} est introuvable sur le miroir.', en: 'Checkpoint {n} cannot be found on the mirror.' },
	'v2.complaint.badcp': { fr: 'Le point de contrôle {n} du miroir n’est pas signé par la clé du registre.', en: 'Checkpoint {n} on the mirror is not signed by the log key.' },
	'v2.complaint.present': { fr: 'Le bulletin figure dans le registre (point de contrôle {n}, entrée {seq}) : la réclamation est close.', en: 'The ballot is in the log (checkpoint {n}, entry {seq}): the complaint is closed.' },
	'v2.complaint.absent': { fr: 'Le bulletin ne figure pas dans les entrées du point de contrôle {n}, signé le {at} : notre serveur a accusé réception d’un bulletin qu’il n’a pas enregistré.', en: 'The ballot is not among the entries of checkpoint {n}, signed on {at}: our server acknowledged a ballot it did not log.' },
	'v2.complaint.verified': { fr: 'Vérifié dans votre navigateur : signature de l’accusé de réception, signature du point de contrôle, entrées du miroir.', en: 'Checked in your browser: the acknowledgement’s signature, the checkpoint’s signature, the mirror’s entries.' },
	'v2.status.verified.help': { fr: 'Votre bulletin figure dans le registre public, chiffré, tel que vous l’avez envoyé. Votre navigateur l’a vérifié.', en: 'Your ballot is in the public log, encrypted, as you sent it. Your browser checked it.' },
	'v2.warm.artifact': { fr: 'Préparation du vote : chargement du circuit…', en: 'Preparing the vote: loading the circuit…' },
	'v2.warm.backend': { fr: 'Préparation du vote : démarrage du prouveur…', en: 'Preparing the vote: starting the prover…' },
	'v2.warm.failed': { fr: 'Le prouveur n’a pas pu démarrer. Rechargez la page.', en: 'The prover could not start. Reload the page.' },
	'v2.join.waiting': { fr: 'Inscription en cours, quelques secondes…', en: 'Registering, a few seconds…' },
	'v2.key.get': { fr: 'Obtenir ma clé de vote', en: 'Get my voting key' },
	'v2.key.title': { fr: 'Votre clé de vote', en: 'Your voting key' },
	'v2.key.intro': { fr: 'Un passeport, une clé, un vote. La clé reste sur vos appareils ; votre passeport sert une fois, pour prouver que vous êtes majeur et français. Il ne quitte pas votre téléphone.', en: 'One passport, one key, one vote. The key stays on your devices; your passport serves once, to prove you are an adult French national. It never leaves your phone.' },
	'v2.key.passkey': { fr: 'Créer ma clé avec Face ID ou mon empreinte', en: 'Create my key with Face ID or my fingerprint' },
	'v2.key.words': { fr: 'Créer ma clé avec 12 mots', en: 'Create my key with 12 words' },
	'v2.key.words.title': { fr: 'Notez ces 12 mots, dans l’ordre.', en: 'Write down these 12 words, in order.' },
	'v2.key.words.warning': { fr: 'Sans eux et sans cet appareil, votre dernier bulletin restera compté, mais vous ne pourrez plus le changer.', en: 'Without them and without this device, your last ballot stays counted, but you can no longer change it.' },
	'v2.key.words.confirm': { fr: 'Saisissez les mots n° {a}, {b} et {c}.', en: 'Enter words {a}, {b} and {c}.' },
	'v2.key.words.bad': { fr: 'Ce ne sont pas les bons mots.', en: 'Those are not the right words.' },
	'v2.key.continue': { fr: 'Continuer', en: 'Continue' },
	'v2.key.posting': { fr: 'Inscription envoyée, attente de l’accusé de réception', en: 'Registration sent, waiting for the acknowledgement' },
	'v2.key.joined': { fr: 'Inscrit. Vous pourrez voter dans quelques secondes.', en: 'Registered. You can vote in a few seconds.' },
	'v2.key.restore': { fr: 'J’ai déjà une clé : mes 12 mots', en: 'I already have a key: my 12 words' },
	'v2.key.restore.placeholder': { fr: 'vos 12 mots', en: 'your 12 words' },
	'v2.key.restore.notfound': { fr: 'Aucune inscription pour ces mots.', en: 'No registration for these words.' },
	'v2.passport.only': { fr: 'Passeport uniquement pour l’instant : une carte d’identité et un passeport donneraient deux voix à la même personne.', en: 'Passports only for now: an identity card and a passport would give the same person two votes.' },
	'v2.reason.stale_seq': { fr: 'Un autre appareil a voté depuis : réessayez.', en: 'Another device voted since: try again.' },
	'v2.reason.old_root': { fr: 'Liste des votants trop ancienne : réessayez.', en: 'Voter list too old: try again.' },
	'v2.reason.bad_proof': { fr: 'Preuve refusée.', en: 'Proof refused.' },
	'v2.reason.closed': { fr: 'Le vote est clos.', en: 'The vote is closed.' },
	'v2.reason.duplicate': { fr: 'Bulletin déjà reçu.', en: 'Ballot already received.' },
	'v2.reason.unknown_setup': { fr: 'Configuration inconnue : rechargez la page.', en: 'Unknown setup: reload the page.' },
	'v2.reason.malformed': { fr: 'Bulletin mal formé.', en: 'Malformed ballot.' },
	'profile.key.passkey': { fr: 'Protégée par votre passkey (Face ID, empreinte), qui la retrouve sur vos autres appareils.', en: 'Protected by your passkey (Face ID, fingerprint), which brings it to your other devices.' },
	'profile.key.words': { fr: 'Sauvegardée par vos 12 mots.', en: 'Backed up by your 12 words.' },
};

// Reactive translator. Pass the current lang value (from get(lang) or a $lang
// subscription) to avoid importing the store into every component just for one call.
export function tr(key: string, currentLang: Lang, vars?: Record<string, string | number>): string {
	const entry = dict[key];
	if (!entry) return key; // surface missing keys as the literal so they're easy to spot
	let s = entry[currentLang] ?? entry.fr ?? key;
	if (vars) {
		for (const [k, v] of Object.entries(vars)) {
			s = s.replaceAll('{' + k + '}', String(v));
		}
	}
	return s;
}

// Convenience: get current lang synchronously (for use inside non-reactive helpers).
export function currentLang(): Lang {
	return get(lang);
}
