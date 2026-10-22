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
	// Brand subtitle stays English in both modes — it's an intentional in-product
	// English-only tagline (looks worse translated literally to French).
	'nav.brand_subtitle': { fr: 'the honesty ledger', en: 'the honesty ledger' },

	// Home / dashboard
	'home.search_placeholder': { fr: 'Rechercher par nom…', en: 'Search by name…' },
	'home.filter.all': { fr: 'Tous', en: 'Both' },
	'home.filter.people': { fr: 'Personnes', en: 'People' },
	'home.filter.parties': { fr: 'Partis', en: 'Parties' },
	'home.most_honest': { fr: 'Les plus honnêtes', en: 'Most Honest' },
	'home.least_honest': { fr: 'Les moins honnêtes', en: 'Least Honest' },
	'home.top_n_by_score': { fr: 'Top {n} par score', en: 'Top {n} by score' },
	'home.bottom_n_by_score': { fr: 'Bas {n} par score', en: 'Bottom {n} by score' },
	'home.all_entities': { fr: 'Toutes les entités', en: 'All entities' },
	'home.no_data': { fr: 'Aucune donnée.', en: 'No data yet.' },
	'home.no_results': { fr: 'Aucun résultat.', en: 'No results.' },
	'home.load_more': { fr: 'Charger plus', en: 'Load More' },
	'home.retry': { fr: 'Réessayer', en: 'Retry' },
	'home.party_badge': { fr: 'Parti', en: 'Party' },

	// Person page
	'person.back': { fr: 'Retour au tableau de bord', en: 'Back to Dashboard' },
	'person.score': { fr: 'Score', en: 'Score' },
	'person.own_score': { fr: 'Son score', en: 'Own score' },
	'person.members_score': { fr: 'Ses membres', en: 'Its members' },
	'person.aggregate': { fr: 'Au total', en: 'Overall' },
	'person.party_badge': { fr: 'Parti', en: 'Party' },
	'person.honest': { fr: 'Honnête', en: 'Honest' },
	'person.mixed': { fr: 'Mitigé', en: 'Mixed' },
	'person.dishonest': { fr: 'Malhonnête', en: 'Dishonest' },
	'person.stances': { fr: 'Positions', en: 'Stances' },
	'person.no_stances': { fr: 'Aucune position pour cette personne pour le moment.', en: 'No stances found for this person yet.' },
	'person.sort_by': { fr: 'Trier par', en: 'Sort by' },
	'person.sort.worst': { fr: 'Pire en premier', en: 'Worst first' },
	'person.sort.recent': { fr: 'Plus récent', en: 'Most recent' },
	'person.sort.most_facts': { fr: 'Plus de faits', en: 'Most facts' },
	'person.no_contradictions_chip': { fr: 'Aucune contradiction pour le moment', en: 'No contradictions yet' },
	'person.updated': { fr: 'Mis à jour', en: 'Updated' },
	'person.history': { fr: 'Évolution dans le temps', en: 'Over time' },
	'person.history.score': { fr: 'Score (cumulé)', en: 'Score (all time)' },
	'person.history.own_score': { fr: 'Score propre (cumulé)', en: 'Own score (all time)' },
	'person.history.recent': { fr: 'Score (12 derniers mois)', en: 'Score (last 12 months)' },
	'person.history.facts': { fr: 'Faits par semaine', en: 'Facts per week' },
	'person.history.facts_total': { fr: 'faits au total', en: 'facts in total' },
	'person.history.empty': { fr: 'Pas encore d’historique.', en: 'No history yet.' },
	'person.history.error': { fr: 'Historique indisponible', en: 'History unavailable' },
	'person.history.range': { fr: 'Période affichée', en: 'Time range' },
	'person.history.range.3m': { fr: '3 mois', en: '3M' },
	'person.history.range.1y': { fr: '1 an', en: '1Y' },
	'person.history.range.all': { fr: 'Tout', en: 'All' },
	'person.history.axis_note': { fr: 'Axe temporel : date de publication des déclarations, par semaine.', en: 'Time axis: statement publication date, weekly.' },
	'person.tab.contradictions': { fr: 'Contradictions', en: 'Contradictions' },
	'person.tab.facts': { fr: 'Faits', en: 'Facts' },
	'person.tab.no_contradictions': { fr: 'Aucune contradiction dans cette position.', en: 'No contradictions in this stance.' },
	'person.tab.no_facts': { fr: 'Aucun fait dans cette position.', en: 'No facts in this stance.' },
	'person.loading': { fr: 'Chargement…', en: 'Loading…' },
	'person.source.original': { fr: 'Voir la source', en: 'Show source' },
	'person.source.archive': { fr: 'Voir l’archive', en: 'Show archive' },
	'fact.reported_n_times': { fr: 'Rapporté {n} fois', en: 'Reported {n} times' },
	'person.show_n_more': { fr: 'Voir {n} de plus', en: 'Show {n} more' },
	'person.show_all_n': { fr: 'Voir tout ({n})', en: 'Show all {n}' },
	'person.retry': { fr: 'Réessayer', en: 'Retry' },
	'person.members': { fr: 'membres', en: 'members' },
	'person.no_persons_found': { fr: 'Aucune personne trouvée', en: 'No persons found' },

	// Contradiction pair (component)
	'contradiction.statement_a': { fr: 'Énoncé A', en: 'Statement A' },
	'contradiction.statement_b': { fr: 'Énoncé B', en: 'Statement B' },
	'contradiction.view_in_facts': { fr: 'Voir dans les faits', en: 'View in Facts' },
	'contradiction.facts_in_tension': { fr: '{n} faits en tension', en: '{n} facts in tension' },
	'contradiction.base_fact': { fr: 'Affirmation de base', en: 'Base claim' },

	// Pre-poll (election page)
	'nav.election': { fr: 'Présidentielle 2027', en: '2027 election' },
	'poll.kicker': { fr: 'Pré-sondage citoyen', en: 'Citizen pre-poll' },
	'poll.disclaimer': { fr: 'Consultation des utilisateurs de tartuffe.me, non représentative de la population. Ce n’est pas un sondage.', en: 'A consultation of tartuffe.me users, not representative of the population. This is not an opinion poll.' },
	'poll.voters': { fr: '{n} votants', en: '{n} voters' },
	'poll.voter': { fr: '1 votant', en: '1 voter' },
	'poll.closes': { fr: 'Vote ouvert jusqu’au {d}', en: 'Voting open until {d}' },
	'poll.closed': { fr: 'Le vote est clos.', en: 'Voting is closed.' },
	'poll.hidden.too_few_voters': { fr: 'Les résultats s’affichent à partir de {min} votants ({n} pour l’instant). Votez et partagez !', en: 'Results show from {min} voters ({n} so far). Vote and share!' },
	'poll.hidden.blackout': { fr: 'Résultats masqués : la loi interdit leur publication la veille et le jour du scrutin.', en: 'Results hidden: the law forbids publishing them the day before and the day of the vote.' },
	'poll.sort': { fr: 'Classer par', en: 'Order by' },
	'poll.sort.preference': { fr: 'Préférence', en: 'Preference' },
	'poll.sort.honesty': { fr: 'Honnêteté', en: 'Honesty' },
	'poll.gauge.preference': { fr: 'Choisi en 1er', en: 'First choice' },
	'poll.gauge.honesty': { fr: 'Honnêteté /100', en: 'Honesty /100' },
	'poll.gauge.votes': { fr: '{n} voix', en: '{n} votes' },
	'poll.gauge.facts': { fr: '{n} faits', en: '{n} facts' },
	'poll.gauge.hidden': { fr: 'masqué', en: 'hidden' },
	'poll.rank.preference': { fr: '{n}e en préférence', en: '#{n} by preference' },
	'poll.rank.preference_first': { fr: '1er en préférence', en: '#1 by preference' },
	'poll.rank.honesty': { fr: '{n}e en honnêteté', en: '#{n} by honesty' },
	'poll.rank.honesty_first': { fr: '1er en honnêteté', en: '#1 by honesty' },
	'poll.top_n': { fr: 'Dans le top {k} de {p} des votants', en: 'In the top {k} of {p} of voters' },
	'poll.view': { fr: 'Affichage', en: 'View' },
	'poll.view.vote': { fr: 'Vote', en: 'Vote' },
	'poll.view.both': { fr: 'Les deux', en: 'Both' },
	'poll.view.honesty': { fr: 'Honnêteté', en: 'Honesty' },
	'poll.note.vote': { fr: 'Part des votants qui placent le candidat en 1er ; la barre est relative au candidat en tête.', en: 'Share of voters who rank the candidate first; the bar is relative to the leader.' },
	'poll.note.honesty': { fr: 'Score d’honnêteté sur 100, calculé à partir des contradictions trouvées dans les déclarations et votes publics. Ce n’est pas un pourcentage de mensonges.', en: 'Honesty score out of 100, computed from the contradictions found in public statements and votes. It is not a percentage of lies.' },
	'poll.gauge.contradictions': { fr: '{n} contradictions', en: '{n} contradictions' },
	'poll.col.vote': { fr: 'Vote', en: 'Vote' },
	'poll.col.candidate': { fr: 'Candidat', en: 'Candidate' },
	'poll.col.honesty': { fr: 'Honnêteté', en: 'Honesty' },
	'poll.top_n_short': { fr: 'top {k} : {p}', en: 'top {k}: {p}' },
	'poll.no_candidates': { fr: 'Les candidats seront ajoutés dès leur déclaration.', en: 'Candidates are added as soon as they declare.' },
	'poll.ballot.sign_in': { fr: 'Se connecter pour voter', en: 'Sign in to vote' },
	'poll.ballot.up': { fr: 'Monter', en: 'Move up' },
	'poll.ballot.down': { fr: 'Descendre', en: 'Move down' },
	'poll.ballot.consent': { fr: 'J’accepte que mon classement, une opinion politique, soit enregistré sous mon identifiant anonyme. Il n’est jamais publié seul : seuls les totaux le sont. Je peux le retirer à tout moment.', en: 'I agree that my ranking, a political opinion, is stored under my anonymous identifier. It is never published on its own: only the totals are. I can withdraw it at any time.' },
	'poll.ballot.saving': { fr: 'Enregistrement…', en: 'Saving…' },
	'poll.ballot.test_account': { fr: 'Compte de test : ce vote n’est pas vérifié.', en: 'Test account: this vote is not verified.' },
	'poll.how.title': { fr: 'Comment lire cette page', en: 'How to read this page' },
	'poll.how.preference': { fr: '<strong>Vote</strong> : la part des votants qui placent ce candidat en 1er. La somme sur tous les candidats fait 100 % : avec beaucoup de candidats, chaque part est petite. La barre est donc relative au candidat en tête, et le rang est affiché à côté. « top 3 » donne la part des votants qui le classent dans leurs 3 choix.', en: '<strong>Vote</strong>: the share of voters who rank this candidate first. It adds up to 100% across candidates: with many candidates each share is small, so the bar is relative to the leader and the rank is shown next to it. “top 3” gives the share of voters who rank them among their 3 choices.' },
	'poll.how.honesty': { fr: '<strong>Score d’honnêteté</strong> : calculé à partir des contradictions trouvées dans les déclarations et les votes publics du candidat, sur 100. Ce n’est pas un pourcentage de mensonges : chaque point se vérifie sur sa fiche, sources à l’appui.', en: '<strong>Honesty score</strong>: computed from the contradictions found in the candidate’s public statements and votes, out of 100. It is not a percentage of lies: every point can be checked on their page, with sources.' },
	'poll.how.compare': { fr: 'Les deux nombres ne se comparent pas directement : comparez plutôt les <strong>rangs</strong>. Un candidat 1er en préférence mais 6e en honnêteté est plus populaire que son bilan.', en: 'The two numbers do not compare directly: compare the <strong>ranks</strong> instead. A candidate #1 by preference but #6 by honesty is more popular than their record.' },
	'poll.how.facts': { fr: 'Le nombre de faits indique la couverture : un score qui repose sur peu de faits est moins sûr.', en: 'The number of facts shows the coverage: a score built on few facts is less certain.' },
	'poll.how.verification': { fr: 'Chaque votant prouve, avec son passeport ou sa carte d’identité, qu’il est un électeur français majeur, sans révéler qui il est. <a href="/verification">Comment ça marche</a>.', en: 'Each voter proves with their passport or ID card that they are an adult French voter, without revealing who they are. <a href="/verification">How it works</a>.' },
	'poll.how.privacy': { fr: 'Votre vote est enregistré sous un identifiant anonyme, jamais publié seul. Les totaux s’affichent à partir d’un nombre minimum de votants et sont masqués pendant la période d’interdiction légale.', en: 'Your vote is stored under an anonymous identifier and never published on its own. Totals show from a minimum number of voters and are hidden during the legal blackout.' },
	'poll.error': { fr: 'Impossible de charger le pré-sondage.', en: 'Could not load the pre-poll.' },
	'poll.not_found': { fr: 'Ce pré-sondage n’existe pas.', en: 'This pre-poll does not exist.' },
	'vote.title': { fr: 'Mes votes', en: 'My votes' },
	'vote.hint': { fr: '{n} votes, dans l’ordre de votre préférence. Modifiables jusqu’à la clôture.', en: '{n} votes, in order of preference. You can change them until voting closes.' },
	'vote.closed_hint': { fr: 'Le vote est clos : vos votes ne peuvent plus changer.', en: 'Voting is closed: your votes can no longer change.' },
	'vote.empty': { fr: 'Vous n’avez pas encore voté. Cliquez sur « Voter » à côté d’un candidat.', en: 'You have not voted yet. Click “Vote” next to a candidate.' },
	'vote.credits': { fr: '{used} / {max} votes utilisés', en: '{used} / {max} votes used' },
	'vote.slot_free': { fr: 'Vote disponible', en: 'Vote available' },
	'vote.add': { fr: 'Voter', en: 'Vote' },
	'vote.mine': { fr: 'Vote n°{n}', en: 'Vote #{n}' },
	'vote.change_button': { fr: 'Changer de vote', en: 'Change vote' },
	'vote.remove': { fr: 'Retirer', en: 'Remove' },
	'vote.remove_named': { fr: 'Retirer mon vote pour {name}', en: 'Remove my vote for {name}' },
	'vote.withdraw_all': { fr: 'Retirer tous mes votes', en: 'Remove all my votes' },
	'vote.withdraw.title': { fr: 'Retirer tous vos votes ?', en: 'Remove all your votes?' },
	'vote.withdraw.text': { fr: 'Vos {n} votes seront retirés. Vous pourrez revoter tant que le vote est ouvert.', en: 'Your {n} votes will be removed. You can vote again while voting is open.' },
	'vote.cancel': { fr: 'Annuler', en: 'Cancel' },
	'vote.consent.title': { fr: 'Avant votre premier vote', en: 'Before your first vote' },
	'vote.consent.accept': { fr: 'J’accepte et je vote pour {name}', en: 'I agree, vote for {name}' },
	'vote.change.title': { fr: 'Changer de vote ?', en: 'Change your vote?' },
	'vote.change.text': { fr: 'Vous avez utilisé vos {n} votes. Pour voter pour {name}, retirez d’abord un vote parmi les suivants :', en: 'You have used your {n} votes. To vote for {name}, first remove a vote from the following:' },
	'vote.change.remove': { fr: 'Retirer ce vote', en: 'Remove this vote' },
	'vote.change.note': { fr: '{name} prendra la place du vote retiré dans votre classement.', en: '{name} takes the place of the vote removed in your ranking.' },
	'vote.change.keep': { fr: 'Garder mes votes', en: 'Keep my votes' },
	'vote.profile_link': { fr: 'Gérer mes votes', en: 'Manage my votes' },
	'vote.done.added': { fr: 'Vote pour {name} enregistré.', en: 'Vote for {name} saved.' },
	'vote.done.removed': { fr: 'Vote pour {name} retiré.', en: 'Vote for {name} removed.' },
	'vote.done.moved': { fr: 'Classement enregistré.', en: 'Ranking saved.' },
	'vote.done.withdrawn': { fr: 'Tous vos votes sont retirés.', en: 'All your votes are removed.' },
	'vote.error.closed': { fr: 'Le vote est clos : vos votes ne peuvent plus changer.', en: 'Voting is closed: your votes can no longer change.' },
	'vote.error.signed_out': { fr: 'Votre session a expiré : reconnectez-vous.', en: 'Your session expired: sign in again.' },
	'vote.error.not_on_ballot': { fr: 'Ce vote avait déjà été retiré (dans un autre onglet ?). Vos votes sont à jour.', en: 'This vote was already removed (in another tab?). Your votes are up to date.' },
	'profile.title': { fr: 'Mon profil', en: 'My profile' },
	'profile.sign_in_text': { fr: 'Connectez-vous pour voir et gérer vos votes.', en: 'Sign in to see and manage your votes.' },
	'profile.account': { fr: 'Mon compte', en: 'My account' },
	'profile.verified': { fr: 'Identité vérifiée : électeur français majeur. Vous pouvez voter. Ni votre nom ni votre numéro de document ne sont connus du site.', en: 'Identity verified: adult French voter. You can vote. The site knows neither your name nor your document number.' },
	'profile.unverified': { fr: 'Identité pas encore vérifiée : il le faut pour voter, une seule fois.', en: 'Identity not verified yet: it is needed to vote, once.' },
	'profile.google': { fr: 'Connecté avec Google. Google ne nous a transmis ni votre e-mail ni votre nom.', en: 'Signed in with Google. Google gave us neither your e-mail nor your name.' },
	'profile.test': { fr: 'Compte de test : vos votes ne sont pas vérifiés.', en: 'Test account: your votes are not verified.' },
	'profile.provider_other': { fr: 'Connecté avec {p}', en: 'Signed in with {p}' },
	'profile.consent_given': { fr: 'Vous avez accepté que vos votes soient enregistrés.', en: 'You agreed that your votes are stored.' },
	'profile.votes': { fr: 'Mes votes', en: 'My votes' },
	'profile.no_events': { fr: 'Aucun vote n’est ouvert pour l’instant.', en: 'No vote is open right now.' },
	'profile.open_until': { fr: 'Ouvert jusqu’au {d}', en: 'Open until {d}' },
	'profile.open': { fr: 'Ouvert', en: 'Open' },
	'profile.closed': { fr: 'Clos : vos votes sont figés', en: 'Closed: your votes are final' },
	'profile.empty': { fr: 'Vous n’avez pas encore voté : vos {n} votes sont disponibles.', en: 'You have not voted yet: your {n} votes are available.' },
	'profile.go_vote': { fr: 'Voter', en: 'Vote' },
	'profile.go_results': { fr: 'Voir les résultats', en: 'See the results' },
	'profile.danger': { fr: 'Supprimer mon compte', en: 'Delete my account' },
	'profile.danger_text': { fr: 'Supprime définitivement votre compte, tous vos votes (y compris ceux des votes clos) et votre consentement. Se reconnecter avec le même document crée un compte vide.', en: 'Permanently deletes your account, all your votes (including those of closed votes) and your consent. Signing in again with the same document creates an empty account.' },
	'profile.delete': { fr: 'Supprimer mon compte et mes votes', en: 'Delete my account and my votes' },
	'profile.delete.title': { fr: 'Supprimer votre compte ?', en: 'Delete your account?' },
	'profile.delete.text': { fr: 'Tous vos votes seront effacés des résultats et votre compte supprimé. Cette action est définitive.', en: 'All your votes will be erased from the results and your account deleted. This cannot be undone.' },
	'profile.deleted': { fr: 'Votre compte et vos votes ont été supprimés.', en: 'Your account and your votes were deleted.' },
	'profile.error': { fr: 'Impossible de charger vos votes.', en: 'Could not load your votes.' },
	'nav.profile': { fr: 'Mon profil', en: 'My profile' },

	// Home promotion of the featured pre-poll
	'home.poll.kicker': { fr: 'Présidentielle 2027', en: '2027 presidential election' },
	'home.poll.title': { fr: 'Votre candidat dit-il vrai\u00a0?', en: 'Does your candidate tell the truth?' },
	'home.poll.lead': { fr: 'Classez vos candidats préférés et voyez, à côté, leur score d’honnêteté mesuré sur leurs propres déclarations et votes.', en: 'Rank your favourite candidates and see, right next to it, their honesty score measured on their own statements and votes.' },
	'home.poll.cta': { fr: 'Voter et comparer', en: 'Vote and compare' },
	'home.poll.candidates': { fr: '{n} candidats', en: '{n} candidates' },

	// ZKPassport sign-in
	'zk.title': { fr: 'Passeport ou carte d’identité', en: 'Passport or ID card' },
	'zk.badge': { fr: '1 personne = 1 voix', en: '1 person = 1 vote' },
	'zk.why': { fr: 'Pourquoi un document d’identité ? Pour qu’une personne ne vote qu’une fois. Sans cela, n’importe qui pourrait créer cent comptes et fausser les résultats. Nous ne voyons ni votre nom ni votre photo.', en: 'Why an ID document? So that each person votes only once. Without it, anyone could create a hundred accounts and skew the results. We never see your name or photo.' },
	'zk.step1': { fr: 'Installez l’app ZKPassport (iOS ou Android).', en: 'Install the ZKPassport app (iOS or Android).' },
	'zk.step2': { fr: 'Scannez le QR code, puis posez votre passeport ou carte d’identité à puce contre le téléphone.', en: 'Scan the QR code, then hold your chip passport or ID card against the phone.' },
	'zk.step3': { fr: 'Acceptez : la preuve est calculée sur le téléphone et vérifiée par nos serveurs.', en: 'Accept: the proof is computed on the phone and checked by our servers.' },
	'zk.start': { fr: 'Vérifier avec mon document', en: 'Verify with my document' },
	'zk.how': { fr: 'Comment ça marche, et ce que nous voyons', en: 'How it works, and what we see' },
	'zk.starting': { fr: 'Préparation de la demande…', en: 'Preparing the request…' },
	'zk.open_app': { fr: 'Ouvrir l’app ZKPassport', en: 'Open the ZKPassport app' },
	'zk.phone_hint': { fr: 'L’app s’ouvre avec la demande ; revenez ici une fois la preuve envoyée.', en: 'The app opens with the request; come back here once the proof is sent.' },
	'zk.qr_alt': { fr: 'QR code de la demande ZKPassport', en: 'QR code of the ZKPassport request' },
	'zk.scan_hint': { fr: 'Scannez ce code avec l’app ZKPassport.', en: 'Scan this code with the ZKPassport app.' },
	'zk.get_app': { fr: 'Pas encore l’app ?', en: 'No app yet?' },
	'zk.cancel': { fr: 'Annuler', en: 'Cancel' },
	'zk.received': { fr: 'Demande reçue sur votre téléphone. Suivez les étapes dans l’app…', en: 'Request received on your phone. Follow the steps in the app…' },
	'zk.proving': { fr: 'Votre téléphone calcule la preuve (quelques secondes)…', en: 'Your phone is computing the proof (a few seconds)…' },
	'zk.verifying': { fr: 'Vérification de la preuve…', en: 'Checking the proof…' },
	'zk.rejected': { fr: 'Demande refusée dans l’app.', en: 'Request declined in the app.' },
	'zk.error': { fr: 'La vérification a échoué.', en: 'Verification failed.' },
	'zk.retry': { fr: 'Recommencer', en: 'Start again' },
	'zk.purpose': { fr: 'Prouver que vous êtes un électeur français majeur pour voter au pré-sondage de tartuffe.me, sans révéler votre identité.', en: 'Prove you are an adult French voter to vote in the tartuffe.me pre-poll, without revealing your identity.' },
	'zk.anim.alt': { fr: 'Animation : votre téléphone lit votre document, n’en garde que « Français » et « 18 ans et + », les scelle dans une preuve et envoie seulement la preuve à nos serveurs.', en: 'Animation: your phone reads your document, keeps only “French” and “18+”, seals them in a proof and sends only the proof to our servers.' },
	'zk.anim.doc': { fr: 'Votre document', en: 'Your document' },
	'zk.anim.stays': { fr: 'reste sur le téléphone', en: 'stays on the phone' },
	'zk.anim.french': { fr: 'Français', en: 'French' },
	'zk.anim.adult': { fr: '18 ans et +', en: '18+' },
	'zk.anim.proof': { fr: 'Preuve', en: 'Proof' },
	'zk.anim.sealed': { fr: 'Preuve scellée', en: 'Sealed proof' },
	'zk.anim.server': { fr: 'Nos serveurs', en: 'Our servers' },
	'zk.anim.never': { fr: 'Nous ne lisons jamais votre passeport.', en: 'We never read your passport.' },
	'zk.anim.only': { fr: 'Nous recevons seulement :', en: 'All we receive:' },
	'zk.anim.and': { fr: 'et', en: 'and' },
	'zk.anim.note': { fr: 'Plus un identifiant anonyme propre à ce site, pour qu’un document ne vote qu’une fois. Nom, photo, date de naissance et numéro ne quittent jamais votre téléphone.', en: 'Plus an anonymous identifier unique to this site, so one document votes once. Name, photo, birth date and number never leave your phone.' },
	'zk.dev_mode': { fr: 'Mode test : les documents fictifs de l’app sont acceptés.', en: 'Test mode: the app’s mock documents are accepted.' },

	// Sign-in
	'auth.google.button': { fr: 'Continuer avec Google', en: 'Continue with Google' },
	'auth.google.private': { fr: 'Google ne nous transmet ni votre e-mail, ni votre nom, ni votre photo : seulement un identifiant anonyme.', en: 'Google gives us neither your e-mail, nor your name, nor your photo: only an anonymous identifier.' },
	'auth.privacy_link': { fr: 'Confidentialité', en: 'Privacy' },
	'auth.google.failed': { fr: 'La connexion avec Google n’a pas abouti.', en: 'The Google sign-in did not go through.' },
	'auth.google.back': { fr: 'Retour au site', en: 'Back to the site' },
	'auth.google.signing_in': { fr: 'Connexion…', en: 'Signing in…' },
	'verify.title': { fr: 'Vérifier mon identité', en: 'Verify my identity' },
	'verify.lead': { fr: 'Une seule fois, pour voter : prouvez que vous êtes un électeur français majeur. Votre nom ne quitte pas votre téléphone.', en: 'Once, to vote: prove you are an adult French voter. Your name never leaves your phone.' },
	'verify.other_document': { fr: 'Ce compte est déjà vérifié avec un autre document.', en: 'This account is already verified with another document.' },
	'verify.moved': { fr: 'Ce document votait déjà depuis un autre compte : ses votes sont maintenant ici.', en: 'This document already voted from another account: its votes are now here.' },
	'verify.noop.title': { fr: 'Document de test', en: 'Test document' },
	'verify.noop.hint': { fr: 'Mode test : un nom suffit comme « document ». Le même nom sur deux comptes déplace les votes.', en: 'Test mode: a name stands for the “document”. The same name on two accounts moves the votes.' },
	'verify.noop.placeholder': { fr: 'Nom du document de test', en: 'Test document name' },
	'verify.noop.submit': { fr: 'Vérifier', en: 'Verify' },
	'poll.ballot.verify': { fr: 'Vérifier mon identité pour voter', en: 'Verify my identity to vote' },
	'auth.title': { fr: 'Se connecter', en: 'Sign in' },
	'auth.signed_in_as': { fr: 'Connecté : {name}', en: 'Signed in: {name}' },
	'auth.sign_out': { fr: 'Se déconnecter', en: 'Sign out' },
	'auth.close': { fr: 'Fermer', en: 'Close' },
	'auth.none': { fr: 'Le vote n’est pas encore ouvert : aucune méthode de connexion n’est activée.', en: 'Voting is not open yet: no sign-in method is enabled.' },
	'auth.noop.title': { fr: 'Compte de test', en: 'Test account' },
	'auth.noop.hint': { fr: 'Pour les tests uniquement : choisissez un pseudonyme, sans vérification. Le même pseudonyme retrouve le même compte.', en: 'For testing only: pick a pseudonym, with no check. The same pseudonym gets the same account back.' },
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
	'nav.about': { fr: 'À propos', en: 'About' },
	'nav.privacy': { fr: 'Confidentialité', en: 'Privacy' },
	'footer.verification': { fr: 'Vérification d’identité', en: 'Identity check' },

	// Share
	'share.title': { fr: 'Partager', en: 'Share' },
	'share.x': { fr: 'Publier sur X', en: 'Post on X' },
	'share.copy': { fr: 'Copier le lien', en: 'Copy link' },
	'share.copied': { fr: 'Lien copié', en: 'Link copied' },
	'share.more': { fr: 'Partager via…', en: 'Share via…' },
	'share.text.person': { fr: '{name} : {score}/100 en cohérence sur tartuffe.me', en: '{name}: {score}/100 for consistency on tartuffe.me' },
	'share.text.stance': { fr: 'Ce que {name} dit et vote sur « {topic} »', en: 'What {name} says and votes on “{topic}”' },
	'share.text.contradiction': { fr: '{name} : « {a} » puis « {b} »', en: '{name}: “{a}”, then “{b}”' },
	'share.text.fact': { fr: '{name} : « {fact} »', en: '{name}: “{fact}”' },

	// Score explanation
	'score.help': { fr: 'Comment ce score est calculé', en: 'How this score is computed' },
	'score.title': { fr: 'Le score de {name}', en: 'The score of {name}' },
	'score.title.stance': { fr: '« {topic} »', en: '“{topic}”' },
	'score.error': { fr: 'Le détail du score n’a pas pu être chargé. Réessayez dans un instant.', en: 'The score details could not be loaded. Try again in a moment.' },
	'score.facts': { fr: 'Faits', en: 'Facts' },
	'score.contradictions': { fr: 'Contradictions', en: 'Contradictions' },
	'score.members': { fr: 'Membres', en: 'Members' },
	'score.what': { fr: 'Score d’honnêteté : il mesure la cohérence entre ce qui est dit et ce qui est fait. Ce n’est pas la vérité.', en: 'Honesty score: it measures how what is said matches what is done. It is not the truth.' },
	'score.more': { fr: 'La méthode en détail', en: 'The method in detail' },

	// Reports
	'report.title.fact': { fr: 'Signaler un fait erroné', en: 'Report a wrong fact' },
	'report.title.contradiction': { fr: 'Signaler une contradiction erronée', en: 'Report a wrong contradiction' },
	'report.done_short': { fr: 'Signalé', en: 'Reported' },
	'report.why.fact': { fr: 'Qu’est-ce qui est faux ? (mal attribué, mal cité, mal résumé, la source ne le dit pas…)', en: 'What is wrong? (misattributed, misquoted, badly summarized, the source does not say it…)' },
	'report.why.contradiction': { fr: 'Pourquoi ces deux faits ne se contredisent-ils pas ? (contextes différents, l’un des faits est faux…)', en: 'Why do these two facts not contradict? (different contexts, one fact is wrong…)' },
	'report.placeholder.fact': { fr: 'Par exemple : dans la vidéo, elle cite un adversaire, ce n’est pas sa position.', en: 'For example: in the video she quotes an opponent, it is not her position.' },
	'report.placeholder.contradiction': { fr: 'Par exemple : le premier vote porte sur la loi de 2019, le second sur un autre texte.', en: 'For example: the first vote is on the 2019 law, the second on another text.' },
	'report.hint': { fr: 'Une IA relit votre signalement avec les sources. Une affirmation sans élément concret ne suffit pas.', en: 'An AI rereads your report with the sources. A claim without concrete evidence is not enough.' },
	'report.submit': { fr: 'Envoyer', en: 'Send' },
	'report.sending': { fr: 'Envoi…', en: 'Sending…' },
	'report.sent': { fr: 'Merci, votre signalement est enregistré.', en: 'Thank you, your report is recorded.' },
	'report.sent_next': { fr: 'Une IA va le relire avec les sources dans les prochaines minutes. Si vous avez raison, l’élément est retiré et la correction est publiée.', en: 'An AI will reread it with the sources in the next minutes. If you are right, the item is removed and the correction is published.' },
	'report.see_profile': { fr: 'Suivre mes signalements', en: 'Follow my reports' },
	'report.error.too_many_reports': { fr: 'Vous avez déjà 20 signalements en attente. Attendez qu’ils soient examinés.', en: 'You already have 20 reports waiting. Wait until they are reviewed.' },
	'report.error.not_found': { fr: 'Cet élément n’existe plus : il a peut-être déjà été corrigé.', en: 'This item no longer exists: it may have been corrected already.' },
	'report.error.invalid_report': { fr: 'Expliquez en au moins 10 caractères ce qui est faux.', en: 'Explain in at least 10 characters what is wrong.' },
	'report.error.error': { fr: 'Le signalement n’a pas pu être envoyé. Réessayez.', en: 'The report could not be sent. Try again.' },
	'report.status.pending': { fr: 'En cours d’examen', en: 'Being reviewed' },
	'report.status.accepted': { fr: 'Corrigé', en: 'Corrected' },
	'report.status.rejected': { fr: 'Maintenu', en: 'Kept' },
	'report.status.ambiguous': { fr: 'En attente d’une relecture humaine', en: 'Waiting for a human review' },
	'report.status.stale': { fr: 'Déjà retiré', en: 'Already removed' },

	// Profile: reports
	'profile.reports': { fr: 'Mes signalements', en: 'My reports' },
	'profile.reports.none': { fr: 'Vous n’avez rien signalé. Un fait ou une contradiction vous semble faux ? Le drapeau à côté permet de le signaler.', en: 'You have not reported anything. A fact or a contradiction looks wrong? The flag next to it reports it.' },
	'profile.reports.judge': { fr: 'Avis de l’IA :', en: 'The AI’s view:' },

	// Feed
	'feed.title': { fr: 'Le fil', en: 'The feed' },
	'feed.sort.hot': { fr: 'À la une', en: 'Top' },
	'feed.sort.new': { fr: 'Récent', en: 'Latest' },
	'feed.hot_end': { fr: 'C’est tout pour la une.', en: 'That’s all for the top.' },
	'feed.hot_empty': { fr: 'Rien de neuf ces deux dernières semaines.', en: 'Nothing new in the last two weeks.' },
	'feed.see_new': { fr: 'Voir tout, du plus récent au plus ancien', en: 'See everything, latest first' },
	'like.do': { fr: '+1 : à mettre en avant', en: '+1: worth seeing' },
	'like.undo': { fr: 'Retirer mon +1', en: 'Take back my +1' },
	'feed.all': { fr: 'Tout', en: 'All' },
	'feed.contradictions': { fr: 'Contradictions', en: 'Contradictions' },
	'feed.facts': { fr: 'Faits', en: 'Facts' },
	'feed.effect': { fr: 'Effet sur le score de {name} aujourd’hui', en: 'Effect on {name}’s score today' },
	'feed.tag.contradiction': { fr: 'Se contredit', en: 'Contradicts' },
	'feed.person': { fr: 'Son fil', en: 'Their feed' },
	'feed.empty': { fr: 'Rien pour l’instant.', en: 'Nothing yet.' },
	'feed.end': { fr: 'Vous êtes arrivé au bout.', en: 'You reached the end.' },
	'feed.error': { fr: 'Le fil n’a pas pu être chargé.', en: 'The feed could not be loaded.' },
	'feed.new': { fr: '{n} nouveaux éléments — afficher', en: '{n} new items — show' },
	'feed.filter.open': { fr: 'Filtres', en: 'Filters' },
	'feed.filter.person': { fr: 'Filtrer par personne…', en: 'Filter by person…' },
	'feed.filter.clear_person': { fr: 'Retirer le filtre de personne', en: 'Remove the person filter' },
	'feed.filter.from': { fr: 'Du', en: 'From' },
	'feed.filter.to': { fr: 'au', en: 'to' },
	'feed.filter.reset': { fr: 'Effacer les filtres', en: 'Clear filters' },
	'feed.empty_filtered': { fr: 'Rien pour ces filtres.', en: 'Nothing for these filters.' },
	'feed.more': { fr: 'Charger la suite', en: 'Load more' },

	// Corrections
	'corrections.title': { fr: 'Corrections', en: 'Corrections' },
	'corrections.intro': { fr: 'Ce que vous avez signalé et qui a été retiré, avec la raison.', en: 'What you reported and we removed, with the reason.' },
	'corrections.kind.fact': { fr: 'Fait retiré', en: 'Fact removed' },
	'corrections.kind.contradiction': { fr: 'Contradiction retirée', en: 'Contradiction removed' },
	'corrections.reason': { fr: 'Raison', en: 'Reason' },
	'corrections.empty': { fr: 'Aucune correction pour l’instant.', en: 'No correction yet.' },
	'corrections.on': { fr: 'corrigé le {d}', en: 'corrected on {d}' },
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
