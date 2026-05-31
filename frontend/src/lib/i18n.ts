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
	'person.history.axis_note': { fr: 'Axe temporel : date de publication des déclarations, par semaine.', en: 'Time axis: statement publication date, weekly.' },
	'person.tab.contradictions': { fr: 'Contradictions', en: 'Contradictions' },
	'person.tab.facts': { fr: 'Faits', en: 'Facts' },
	'person.tab.no_contradictions': { fr: 'Aucune contradiction dans cette position.', en: 'No contradictions in this stance.' },
	'person.tab.no_facts': { fr: 'Aucun fait dans cette position.', en: 'No facts in this stance.' },
	'person.loading': { fr: 'Chargement…', en: 'Loading…' },
	'person.source': { fr: 'Source', en: 'Source' },
	'person.source.original': { fr: 'Voir la source', en: 'Show source' },
	'person.source.archive': { fr: 'Voir l’archive', en: 'Show archive' },
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

	// About / transparency page
	'nav.about': { fr: 'À propos', en: 'About' },
	'about.title': { fr: 'Comment ça marche', en: 'How it works' },
	'about.subtitle': { fr: "Transparence sur les données, les sources et le calcul du score d'honnêteté.", en: 'Transparency on data, sources, and how the honesty score is computed.' },
	'about.stats.title': { fr: 'En chiffres', en: 'By the numbers' },
	'about.stats.persons': { fr: 'personnalités suivies', en: 'figures tracked' },
	'about.stats.facts': { fr: 'faits indexés', en: 'facts indexed' },
	'about.stats.contradictions': { fr: 'contradictions détectées', en: 'contradictions detected' },
	'about.stats.last_updated': { fr: 'Dernière mise à jour', en: 'Last updated' },
	'about.stats.never': { fr: 'aucune donnée', en: 'no data yet' },
	'about.scoring.title': { fr: 'Calcul du score', en: 'Score calculation' },
	'about.scoring.intro': { fr: "Le score d'honnêteté va de 0 (totalement malhonnête) à 100 (aucune contradiction détectée). Il décroît exponentiellement avec le nombre de contradictions.", en: 'The honesty score ranges from 0 (fully dishonest) to 100 (no contradictions detected). It decays exponentially with the number of contradictions.' },
	'about.scoring.formula_label': { fr: 'Formule', en: 'Formula' },
	'about.scoring.terms.title': { fr: 'Termes', en: 'Terms' },
	'about.scoring.term.bundles': { fr: 'contradictions groupées (une position distincte violée)', en: 'grouped contradictions (one distinct stance violated)' },
	'about.scoring.term.facts': { fr: "faits vérifiés (plancher de {n} pour les personnalités récentes)", en: 'verified facts (floor of {n} for new figures)' },
	'about.scoring.term.extras': { fr: 'paires supplémentaires dans le même groupe (pénalité de répétition)', en: 'extra pairs within the same bundle (repetition penalty)' },
	'about.scoring.note': { fr: "Deux contradictions sur le même sujet (e.g. promise A vs faits B et C) comptent comme un bundle, non comme deux. Cela évite de pénaliser plusieurs fois la même incohérence.", en: 'Two contradictions on the same topic (e.g. promise A vs facts B and C) count as one bundle, not two. This avoids penalising the same inconsistency multiple times.' },
	'about.detection.title': { fr: 'Détection des contradictions', en: 'Contradiction detection' },
	'about.detection.body': { fr: "Chaque nouveau fait est comparé aux faits existants de la même personnalité via une recherche par similarité vectorielle (pgvector). Les paires candidates sont ensuite soumises à un modèle de langage (LLM) qui applique un ensemble de règles strictes pour juger si les deux faits se contredisent réellement. Un second passage adversarial rejette les faux positifs courants : mesures différentes, sous-ensembles procéduraux, conditions remplies, déclarations dans le même sens.", en: "Each new fact is compared against existing facts for the same figure using vector similarity search (pgvector). Candidate pairs are then sent to a language model (LLM) that applies a strict rule set to judge whether the two facts genuinely contradict each other. A second adversarial pass rejects common false positives: different measurements, procedural sub-scopes, met conditions, same-direction statements." },
	'about.sources.title': { fr: 'Sources de données', en: 'Data sources' },
	'about.sources.news': { fr: 'Recherche d'actualités', en: 'News search' },
	'about.sources.news.body': { fr: "Articles de presse récupérés via Brave Search, Serper ou SerpAPI, puis extraits par un crawler web. Les faits sont extraits du texte brut par un LLM.", en: 'News articles retrieved via Brave Search, Serper or SerpAPI, then scraped by a web crawler. Facts are extracted from the raw text by an LLM.' },
	'about.sources.parliament': { fr: 'Données parlementaires', en: 'Parliamentary data' },
	'about.sources.parliament.body': { fr: "Votes et déclarations de l'Assemblée nationale française téléchargés via les APIs officielles de l'Assemblée (open data).", en: "Votes and statements from the French Assemblée Nationale downloaded via the official Assemblée open-data APIs." },
	'about.sources.twitter': { fr: 'Réseaux sociaux', en: 'Social media' },
	'about.sources.twitter.body': { fr: "Publications officielles sur X/Twitter des personnalités suivies, récupérées périodiquement.", en: 'Official X/Twitter posts from tracked figures, retrieved periodically.' },
	'about.opendata.title': { fr: 'Transparence et erreurs', en: 'Transparency and errors' },
	'about.opendata.body': { fr: "Le système peut produire des faux positifs. Si vous pensez qu'une contradiction est erronée, signalez-la via le formulaire de rapport sur la page de la personnalité concernée. Les scores ne constituent pas une opinion politique — ils mesurent la cohérence des déclarations publiques, pas la valeur des positions.", en: 'The system can produce false positives. If you believe a contradiction is incorrect, report it via the report form on the relevant person page. Scores do not constitute a political opinion — they measure the consistency of public statements, not the merit of positions.' },

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
