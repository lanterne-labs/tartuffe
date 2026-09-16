package backend

import "time"

// Fact represents a single structured factual statement extracted from content.
// It includes the people involved in this specific statement, the normalized fact,
// and the original quote from the source.
type Fact struct {
	// SubjectEntity is the name of the primary person, party, or organization this fact is ABOUT.
	// This is the entity whose actions/statements are being described.
	// Example: "Marine Le Pen" for a fact about her voting record.
	SubjectEntity string `json:"subject_entity,omitempty"`
	// SubjectEntityType is the type of entity: "human", "party", or "organization".
	// Used when creating new persons in the database.
	SubjectEntityType string `json:"subject_entity_type,omitempty"`
	// NormalizedFactText is the core factual claim extracted and normalized by the LLM,
	// written in the source's NATIVE LANGUAGE (e.g. French for French politicians).
	// Includes person names; used for display and LLM contradiction comparison.
	// Example: "Marine Le Pen veut baisser les impôts sur le revenu".
	NormalizedFactText string `json:"normalized_fact"`
	// NormalizedFactTextEn is the English translation of NormalizedFactText.
	// Empty when the source is already English. Both versions are persisted so the
	// frontend can offer a 🌐 toggle and so downstream consumers in other languages
	// can read facts without re-translating.
	NormalizedFactTextEn string `json:"normalized_fact_en,omitempty"`
	// Lang is the BCP-47 source language of the normalized fact ("fr", "en", ...).
	// REQUIRED on every extracted fact. The LLM detects it from the source content,
	// not from the entity's nationality.
	Lang string `json:"lang"`
	// EmbeddingText is a canonical semantic fingerprint used ONLY for generating vector embeddings.
	// Always English, using a fixed canonical verb + broad policy domain keywords.
	// Example: "PROMISED 15-percent-tax-cut income-tax"
	// This enables both dedup (same meaning → near-identical vector) and
	// contradiction detection (opposing positions → same neighborhood).
	EmbeddingText string `json:"embedding_text"`
	// OriginalQuote is the original sentence or snippet from the source document
	// corresponding to the NormalizedFactText. Provides context and verification.
	// Example: "In a fiery speech yesterday, Person X declared their unwavering intention to slash income taxes..."
	OriginalQuote string `json:"original_quote"`
	// CanonicalVerb is one of 20 canonical verbs (DENIED, CLAIMED, PROMISED, etc.)
	// extracted separately from the embedding text for verb-aware dedup/contradiction filtering.
	CanonicalVerb string `json:"canonical_verb,omitempty"`
	// InvolvedPeople lists other people mentioned in the fact (not the subject entity).
	// These are secondary people involved in or relevant to the statement.
	InvolvedPeople []string `json:"involved_people"`
	// SourceURL is the URL where the statement was found.
	SourceURL string `json:"source_url"`
	// PublishedAt is the FINAL publication date attached to this fact's source,
	// resolved by the plugin from whatever sources it has (LLM-extracted body
	// date, HTML <meta>, SERP date, tweet CreatedAt, vote date, ...) via
	// core.PickPublishedAt. This is what eventually lands in fact_sources.published_at.
	PublishedAt *time.Time `json:"published_at"`
	// ArticlePublishedAt is the OPTIONAL date the LLM extracted from the
	// article/tweet body itself (e.g. "Posted on 19 May 2026", "il y a 2 jours"
	// resolved against the prompt's TODAY's date). One input among others to
	// the plugin's PickPublishedAt call — not the final value. Nil when the LLM
	// did not find an explicit date in the body.
	ArticlePublishedAt *time.Time `json:"article_published_at,omitempty"`
	// Embedding is the vector embedding of the EmbeddingText (optional, usually filled later).
	Embedding []float32 `json:"embedding,omitempty"`
}

// ChunkingParameters defines the parameters for chunking text before fact extraction.
type ChunkingParameters struct {
	ThresholdChars int // Content length (in chars) above which to chunk
	ChunkSizeChars int // Target size for each chunk (in chars)
	OverlapChars   int // Character overlap between chunks
}
