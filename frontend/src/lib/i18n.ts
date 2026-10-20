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
	'person.own_score': { fr: 'Score propre', en: 'Own Score' },
	'person.members_score': { fr: 'Score des membres', en: 'Members Score' },
	'person.aggregate': { fr: 'Agrégat', en: 'Aggregate' },
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
	'poll.no_candidates': { fr: 'Les candidats seront ajoutés dès leur déclaration.', en: 'Candidates are added as soon as they declare.' },
	'poll.ballot.title': { fr: 'Mon classement', en: 'My ranking' },
	'poll.ballot.hint': { fr: 'Choisissez jusqu’à {n} candidats, dans l’ordre de votre préférence. Seul votre 1er choix compte dans le pourcentage ; le reste sert au « top {n} ».', en: 'Pick up to {n} candidates, in order of preference. Only your first choice counts in the percentage; the others count in the “top {n}”.' },
	'poll.ballot.sign_in': { fr: 'Se connecter pour voter', en: 'Sign in to vote' },
	'poll.ballot.empty': { fr: 'Aucun candidat choisi. Utilisez « Choisir » sur les cartes ci-dessous.', en: 'No candidate yet. Use “Pick” on the cards below.' },
	'poll.ballot.add': { fr: 'Choisir', en: 'Pick' },
	'poll.ballot.added': { fr: 'Choix n°{n}', en: 'Choice #{n}' },
	'poll.ballot.full': { fr: 'Classement complet', en: 'Ranking full' },
	'poll.ballot.up': { fr: 'Monter', en: 'Move up' },
	'poll.ballot.down': { fr: 'Descendre', en: 'Move down' },
	'poll.ballot.remove': { fr: 'Retirer', en: 'Remove' },
	'poll.ballot.consent': { fr: 'J’accepte que mon classement, une opinion politique, soit enregistré sous mon identifiant anonyme. Il n’est jamais publié seul : seuls les totaux le sont. Je peux le retirer à tout moment.', en: 'I agree that my ranking, a political opinion, is stored under my anonymous identifier. It is never published on its own: only the totals are. I can withdraw it at any time.' },
	'poll.ballot.save': { fr: 'Enregistrer mon vote', en: 'Save my vote' },
	'poll.ballot.saving': { fr: 'Enregistrement…', en: 'Saving…' },
	'poll.ballot.saved': { fr: 'Vote enregistré. Vous pouvez le changer jusqu’à la clôture.', en: 'Vote saved. You can change it until voting closes.' },
	'poll.ballot.withdraw': { fr: 'Retirer mon vote', en: 'Withdraw my vote' },
	'poll.ballot.unsaved': { fr: 'Modifications non enregistrées', en: 'Unsaved changes' },
	'poll.ballot.test_account': { fr: 'Compte de test : ce vote n’est pas vérifié.', en: 'Test account: this vote is not verified.' },
	'poll.how.title': { fr: 'Comment lire cette page', en: 'How to read this page' },
	'poll.how.preference': { fr: '<strong>Choisi en 1er</strong> : la part des votants qui placent ce candidat en tête de leur classement. La somme sur tous les candidats fait 100 %.', en: '<strong>First choice</strong>: the share of voters who put this candidate at the top of their ranking. It adds up to 100% across candidates.' },
	'poll.how.honesty': { fr: '<strong>Score d’honnêteté</strong> : calculé à partir des contradictions trouvées dans les déclarations et les votes publics du candidat, sur 100. Ce n’est pas un pourcentage de mensonges : chaque point se vérifie sur sa fiche, sources à l’appui.', en: '<strong>Honesty score</strong>: computed from the contradictions found in the candidate’s public statements and votes, out of 100. It is not a percentage of lies: every point can be checked on their page, with sources.' },
	'poll.how.compare': { fr: 'Les deux nombres ne se comparent pas directement : comparez plutôt les <strong>rangs</strong>. Un candidat 1er en préférence mais 6e en honnêteté est plus populaire que son bilan.', en: 'The two numbers do not compare directly: compare the <strong>ranks</strong> instead. A candidate #1 by preference but #6 by honesty is more popular than their record.' },
	'poll.how.facts': { fr: 'Le nombre de faits indique la couverture : un score qui repose sur peu de faits est moins sûr.', en: 'The number of facts shows the coverage: a score built on few facts is less certain.' },
	'poll.how.privacy': { fr: 'Votre vote est enregistré sous un identifiant anonyme, jamais publié seul. Les totaux s’affichent à partir d’un nombre minimum de votants et sont masqués pendant la période d’interdiction légale.', en: 'Your vote is stored under an anonymous identifier and never published on its own. Totals show from a minimum number of voters and are hidden during the legal blackout.' },
	'poll.error': { fr: 'Impossible de charger le pré-sondage.', en: 'Could not load the pre-poll.' },
	'poll.not_found': { fr: 'Ce pré-sondage n’existe pas.', en: 'This pre-poll does not exist.' },

	// Home promotion of the featured pre-poll
	'home.poll.kicker': { fr: 'Présidentielle 2027', en: '2027 presidential election' },
	'home.poll.title': { fr: 'Votre candidat dit-il vrai\u00a0?', en: 'Does your candidate tell the truth?' },
	'home.poll.lead': { fr: 'Classez vos candidats préférés et voyez, à côté, leur score d’honnêteté mesuré sur leurs propres déclarations et votes.', en: 'Rank your favourite candidates and see, right next to it, their honesty score measured on their own statements and votes.' },
	'home.poll.cta': { fr: 'Voter et comparer', en: 'Vote and compare' },
	'home.poll.candidates': { fr: '{n} candidats', en: '{n} candidates' },

	// Sign-in
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
