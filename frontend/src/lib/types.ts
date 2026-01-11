// API response types

export interface PersonSummary {
	id: number;
	name: string;
	entity_type?: string;
	honesty_score: number;
}

export interface FactWithSource {
	fact_id: number;
	fact_text: string;
	source_url: string;
	original_quote: string;
}

export interface ContradictionWithFacts {
	id: number;
	person_id: number;
	fact_a_id: number;
	fact_b_id: number;
	fact_a_text: string;
	fact_b_text: string;
	reason: string;
	detected_at: string;
}

export interface EventData {
	id: number;
	topic: string;
	entity_type: string;
	entity_id: number;
	payload: any;
	created_at: string;
}