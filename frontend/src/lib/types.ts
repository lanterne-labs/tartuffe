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

export interface BundledContradictionFact {
	id: number;
	text: string;
	text_en?: string;
	source_urls: string[];
	date: string | null;
	stance_id: number | null;
	stance_label: string | null;
	stance_label_en?: string | null;
}

// One logical inconsistency, potentially backed by multiple pairwise DB rows.
export interface BundledContradiction {
	bundle_id: number | null;
	bundle_label: string | null;
	bundle_label_en?: string | null;
	bundle_summary: string | null;
	bundle_summary_en?: string | null;
	pair_count: number;
	reason: string;
	reason_en?: string;
	detected_at: string;
	facts: BundledContradictionFact[];
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