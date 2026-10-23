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
	// The raw pair count the stance score is computed from.
	contradiction_pairs?: number;
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

// Polls (backend/polls)

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
	// voters counts the ballots, blank votes included: the shares are of them.
	voters: number;
	// blank and blank_share: the blank votes (null while hidden).
	blank: number | null;
	blank_share: number | null;
	// window: the 30-day window the results count, until its end.
	window: { series: number; window: number };
	until: string;
	open: boolean;
	hidden?: 'too_few_voters' | 'blackout';
	candidates: CandidateResult[];
}

export interface PollResponse {
	event: PollEvent;
	results: PollResults;
	// windows: the open windows a ballot is cast in.
	windows: { series: number; window: number; start: string; end: string }[];
}

// Accounts (backend/accounts): an account provider (google) opens the
// account; a verification provider (zkpassport) proves the document that
// admits a voting key, which no account holds; noop (tests) does both.

export type ProviderRole = 'account' | 'verification' | 'test';

export interface AuthProvider {
	name: string;
	role: ProviderRole;
	verified: boolean;
	config?: Record<string, unknown>;
}

export interface AppUser {
	id: number;
	provider: string;
	display_name: string;
}

// GET /api/person/{id}/score: every input of the honesty score.
export interface ScoreExplanation {
	person_id: number;
	entity_type: string;
	facts: number;
	bundles: number;
	pairs: number;
	extras: number;
	denominator: number;
	min_denominator: number;
	ratio_decay: number;
	absolute_decay: number;
	extra_decay: number;
	own_score: number;
	members_score?: number | null;
	members: number;
	score: number;
}

// One fact of a feed item.
export interface FeedFactSide {
	id: number;
	text: string;
	text_en?: string | null;
	date?: string | null;
	source_urls: string[];
	quote?: string;
}

// GET /api/feed: a new contradiction or a new fact.
export interface FeedItem {
	kind: 'contradiction' | 'fact';
	id: number;
	at: string;
	person: { id: number; name: string; image_url?: string | null; honesty_score: number };
	stance_id?: number | null;
	stance_label?: string | null;
	stance_label_en?: string | null;
	fact: FeedFactSide;
	other?: FeedFactSide | null;
	reason?: string | null;
	reason_en?: string | null;
	// likes counts the +1 of the users; liked says the viewer gave one.
	likes: number;
	liked: boolean;
	// score_effect: what the item does to the person's score today, in
	// percent (absent when under 0.1 %).
	score_effect?: number | null;
}

export interface FeedPage {
	items: FeedItem[];
	next?: string;
}

export type ShareKind = 'person' | 'stance' | 'contradiction' | 'fact';

// GET /api/share/{kind}/{id}: what a shared link points at.
export interface ShareTarget {
	kind: ShareKind;
	id: number;
	person_id: number;
	person_name: string;
	image_url?: string | null;
	honesty_score: number;
	fact_count: number;
	contradiction_count: number;
	stance_id?: number | null;
	stance_label?: string | null;
	stance_label_en?: string | null;
	text: string;
	text_en?: string | null;
	other?: string | null;
	quote?: string;
}

export type ReportKind = 'fact' | 'contradiction';
export type ReportStatus = 'pending' | 'accepted' | 'rejected' | 'ambiguous' | 'stale';

// A signed-in user's report (POST /api/reports, GET /api/me/reports).
export interface UserReport {
	id: number;
	target_kind: ReportKind;
	target_id: number;
	person_id: number;
	person_name: string;
	target_text: string;
	reason: string;
	status: ReportStatus;
	verdict_reason: string;
	created_at: string;
	judged_at?: string | null;
}

export interface MyReports {
	reports: UserReport[];
	reported: string[];
}

// GET /api/corrections: an accepted report in the public log.
export interface Correction {
	id: number;
	target_kind: ReportKind;
	person_id: number;
	person_name: string;
	target_text: string;
	verdict_reason: string;
	reported_at: string;
	corrected_at: string;
}
