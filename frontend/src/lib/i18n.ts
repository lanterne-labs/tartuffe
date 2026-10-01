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
			s = s.replace('{' + k + '}', String(v));
		}
	}
	return s;
}

// Convenience: get current lang synchronously (for use inside non-reactive helpers).
export function currentLang(): Lang {
	return get(lang);
}
