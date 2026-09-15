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
