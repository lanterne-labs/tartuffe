package backend

import (
	"context"
	"digital-mirror/backend/extractor"
	"time"
)

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
	// NormalizedFactText is the core factual claim extracted and normalized by the LLM.
	// This version INCLUDES person names and is used for display and LLM contradiction comparison.
	// Example: "Marine Le Pen plans to cut income taxes".
	NormalizedFactText string `json:"normalized_fact"`
	// EmbeddingText is a name-free version of the fact used ONLY for generating vector embeddings.
	// This version REMOVES person/organization names to ensure facts cluster by topic, not by person.
	// Example: "Plans to cut income taxes during political speech"
	// This enables both proper deduplication (same topic + same stance = duplicate) and
	// contradiction detection (same topic + different stance = neighbors to compare).
	EmbeddingText string `json:"embedding_text"`
	// OriginalQuote is the original sentence or snippet from the source document
	// corresponding to the NormalizedFactText. Provides context and verification.
	// Example: "In a fiery speech yesterday, Person X declared their unwavering intention to slash income taxes..."
	OriginalQuote string `json:"original_quote"`
	// Topics are relevant keywords or categories for the statement.
	Topics []string `json:"topics"`
	// InvolvedPeople lists other people mentioned in the fact (not the subject entity).
	// These are secondary people involved in or relevant to the statement.
	InvolvedPeople []string `json:"involved_people"`
	// SourceURL is the URL where the statement was found.
	SourceURL string `json:"source_url"`
	// PublishedAt is the publication date of the source content, if available.
	PublishedAt *time.Time `json:"published_at"`
	// Embedding is the vector embedding of the EmbeddingText (optional, usually filled later).
	Embedding []float32 `json:"embedding,omitempty"`
}

// ChunkingParameters defines the parameters for chunking text before fact extraction.
type ChunkingParameters struct {
	ThresholdChars int // Content length (in chars) above which to chunk
	ChunkSizeChars int // Target size for each chunk (in chars)
	OverlapChars   int // Character overlap between chunks
}

// FactExtractor defines the interface for extracting structured facts from text content using an LLM.
type FactExtractor interface {
	// ExtractFacts processes the given ExtractedContent and returns a slice of Fact structs.
	// It needs the original personName searched for context in prompt generation.
	ExtractFacts(ctx context.Context, personName string, content extractor.ExtractedContent) ([]Fact, error)

	// GetChunkingParameters returns the recommended chunking parameters for the specific LLM implementation.
	GetChunkingParameters() ChunkingParameters
}
