// API response types

export interface PersonSummary {
	id: number;
	name: string;
	entity_type?: string;
	honesty_score: number;
	own_score?: number;
	members_score?: number;
	fact_count: number;
	stance_count: number;
	contradiction_count: number;
	image_url?: string;
}

export interface FactWithSource {
	fact_id: number;
	fact_text: string;
	fact_text_en?: string; // English translation; absent/empty when source is English
	lang?: string;         // BCP-47 source language
	source_url: string;
	original_quote: string;
	archive_url?: string | null; // Wayback snapshot URL; null until archived
}

// Another wording of a fact's claim (a member of its claim group), with its
// own source.
export interface Restatement {
	fact_text: string;
	fact_text_en?: string;
	source_url: string;
	original_quote?: string;
}

export interface ContradictionWithFacts {
	id: number;
	person_id: number;
	fact_a_id: number;
	fact_b_id: number;
	fact_a_text: string;
	fact_a_text_en?: string;
	fact_b_text: string;
	fact_b_text_en?: string;
	fact_a_source_urls: string[];
	fact_b_source_urls: string[];
	fact_a_date: string | null;
	fact_b_date: string | null;
	reason: string;
	reason_en?: string;
	detected_at: string;
	// Bundle (stance-based grouping). bundle_id is the stance id; null = solo.
	// bundle_size counts contradictions in the same bundle for this person.
	bundle_id: number | null;
	bundle_label: string | null;
	bundle_label_en?: string | null;
	bundle_summary: string | null;
	bundle_summary_en?: string | null;
	bundle_size: number;
	// Per-fact stance labels — both non-null + bundle_id null means cross-topic
	// (the two facts contradict across different topic stances). Either one null
	// is a data-quality flag: a fact is missing its stance row.
	fact_a_stance_id: number | null;
	fact_b_stance_id: number | null;
	fact_a_stance_label: string | null;
	fact_a_stance_label_en?: string | null;
	fact_b_stance_label: string | null;
	fact_b_stance_label_en?: string | null;
}

// One spoke of a contradiction group: a fact that conflicts with the group's base
// fact, with the reason it conflicts.
export interface ContradictingFact {
	contradiction_id: number;
	fact_id: number;
	fact_text: string;
	fact_text_en?: string;
	fact_date: string | null;
	source_urls: string[];
	restatements: Restatement[];
	reason: string;
	reason_en?: string;
}

// A distinct inconsistency: one base (anchor) fact contradicted by N facts. Returned
// by GET /api/stances/{id}/contradictions, one card per group. `summary` is the
// LLM-synthesized merged reason (null for a single-fact group — use the spoke reason).
export interface ContradictionGroup {
	base_fact_id: number;
	base_fact_text: string;
	base_fact_text_en?: string;
	base_fact_date: string | null;
	base_fact_source_urls: string[];
	base_restatements: Restatement[];
	summary: string | null;
	summary_en?: string | null;
	contradicting_facts: ContradictingFact[];
}

export interface PaginatedResponse<T> {
	items: T[];
	total: number;
}

export interface StanceDocument {
	id: number;
	label: string;
	label_en?: string;
	summary: string;
	summary_en?: string;
	fact_count: number;
	contradiction_count: number;
	score: number;
	updated_at: string;
}

export interface EventData {
	id: number;
	topic: string;
	entity_type: string;
	entity_id: number;
	payload: any;
	created_at: string;
}
// One weekly bucket of a person's honesty timeline (statement-time axis).
// Date is the bucket start; counts and scores are the state at the end of that week.
export interface HistoryPoint {
	date: string;
	facts_added: number;
	facts_total: number;
	contradictions: number;
	score: number;
	recent_score: number;
}

// Pre-polls (backend/poll, backend/api/polls.go)

export interface PollBlackout {
	starts_at: string;
	ends_at: string;
}

export interface PollEvent {
	id: number;
	slug: string;
	title_fr: string;
	title_en: string;
	description_fr: string;
	description_en: string;
	max_choices: number;
	min_voters: number;
	opens_at: string;
	closes_at?: string;
	featured: boolean;
	blackouts: PollBlackout[];
}

// One candidate's line of the results. The counts, shares and preference rank
// are null while the results are hidden.
export interface CandidateResult {
	person_id: number;
	name: string;
	image_url?: string;
	honesty_score: number;
	fact_count: number;
	contradiction_count: number;
	first_choices: number | null;
	first_choice_share: number | null;
	chosen_share: number | null;
	preference_rank: number | null;
	honesty_rank: number;
}

export interface PollResults {
	voters: number;
	open: boolean;
	hidden?: 'too_few_voters' | 'blackout';
	candidates: CandidateResult[];
}

export interface PollResponse {
	event: PollEvent;
	results: PollResults;
}

export interface Ballot {
	person_ids: number[];
	consented: boolean;
}

// Sign-in (backend/auth)

export interface AuthProvider {
	name: string;
	verified: boolean;
	config?: Record<string, unknown>;
}

export interface AppUser {
	id: number;
	provider: string;
	display_name: string;
	consent_at?: string;
}
