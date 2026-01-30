package core

import (
	"context"
	"digital-mirror/backend/metrics"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
)

// FactInserter handles intelligent fact insertion with similarity detection.
// It automatically detects similar facts using embedding similarity and stores
// only one "canonical fact" for contradiction detection, while preserving all
// related/similar facts for traceability.
type FactInserter struct {
	storer              Storer
	similarityThreshold float64 // L2 distance threshold (default: 0.80)
}

// Storer defines the interface for storage operations needed by FactInserter.
// This is typically implemented by storage.PostgresStorer.
type Storer interface {
	// FindOrInsertPersons resolves person names to person IDs, creating new records if needed.
	// New persons are created with entity_type='human' by default.
	FindOrInsertPersons(ctx context.Context, names []string) ([]int64, error)

	// FindOrInsertPersonWithType resolves a person name to a person ID, creating a new record if needed.
	// If the person doesn't exist, it creates one with the specified entity type (human/party/organization).
	FindOrInsertPersonWithType(ctx context.Context, name string, entityType string) (int64, error)

	// FindSimilarCanonicalFacts searches for canonical facts similar to the given embedding
	// for the specified persons, using L2 distance threshold.
	FindSimilarCanonicalFacts(ctx context.Context, personIDs []int64, embedding []float32, threshold float64) ([]SimilarFact, error)

	// InsertCanonicalFact inserts a new canonical fact (is_canonical=true, canonical_fact_id=NULL).
	InsertCanonicalFact(ctx context.Context, input StoreFactInput) (factID int64, err error)

	// InsertRelatedFact inserts a related/similar fact (is_canonical=false, canonical_fact_id=<id>).
	InsertRelatedFact(ctx context.Context, input StoreFactInput, canonicalFactID int64) (factID int64, err error)

	// LinkSourceToFact links a source URL to a fact, creating a fact_sources record.
	LinkSourceToFact(ctx context.Context, factID int64, sourceURL string, originalQuote string, publishedAt *time.Time, sourceType string, metadata map[string]any) (sourceID int64, err error)

	// LinkPersonsToSourceMention links persons to a fact source, creating fact_mentions records.
	LinkPersonsToSourceMention(ctx context.Context, sourceID int64, personIDs []int64) error
}

// StoreFactInput encapsulates the data needed to store a fact and its source.
type StoreFactInput struct {
	SubjectEntity      string    // Primary entity this fact is about (resolved to person ID for linking)
	SubjectEntityType  string    // Entity type: "human", "party", or "organization" (used when creating new persons)
	NormalizedFactText string    // Full fact text with names (for display and LLM comparison)
	EmbeddingText      string    // Name-free version used ONLY for embedding generation
	Embedding          []float32 // Vector embedding (generated from EmbeddingText)
	OriginalQuote      string
	Topics             []string
	InvolvedPeople     []string // Secondary people mentioned in the fact (not the subject)
	SourceURL          string
	PublishedAt        *time.Time
	SourceType         string
	Metadata           map[string]any
}

// SimilarFact represents a fact that is similar to a given embedding.
type SimilarFact struct {
	ID       int64   // Fact ID
	Text     string  // Fact text
	Distance float64 // Cosine distance (1 - cosine_similarity) from query embedding
}

// NewFactInserter creates a new FactInserter with the given storer.
// The similarity threshold is the minimum cosine similarity (0-1) to consider facts as "the same".
// It is loaded from the FACT_SIMILARITY_THRESHOLD env var, defaulting to 0.90 (90% similar).
// The threshold is converted to cosine distance (1 - similarity) for use in pgvector queries.
func NewFactInserter(storer Storer) *FactInserter {
	cosineSimilarity := 0.90 // default: 90% similar (stricter to avoid false groupings)
	if env := os.Getenv("FACT_SIMILARITY_THRESHOLD"); env != "" {
		if t, err := strconv.ParseFloat(env, 64); err == nil {
			cosineSimilarity = t
			log.Info().Float64("cosine_similarity", cosineSimilarity).Msg("Loaded FACT_SIMILARITY_THRESHOLD from environment")
		} else {
			log.Warn().Str("value", env).Err(err).Msg("Invalid FACT_SIMILARITY_THRESHOLD, using default 0.80")
		}
	}

	// Convert cosine similarity to cosine distance for pgvector
	// cosine_distance = 1 - cosine_similarity
	// So for 80% similarity (0.80), we want cosine distance <= 0.20
	cosineDistance := 1.0 - cosineSimilarity

	return &FactInserter{
		storer:              storer,
		similarityThreshold: cosineDistance,
	}
}

// InsertFacts processes a batch of facts and intelligently inserts them
// as canonical or related facts based on embedding similarity.
// For each fact, it:
// 1. Searches for similar canonical facts associated with the same persons
// 2. If found, inserts as a related fact linked to the canonical fact
// 3. If not found, inserts as a new canonical fact
// All facts preserve their source and person mention links for traceability.
func (fi *FactInserter) InsertFacts(ctx context.Context, facts []StoreFactInput) error {
	for i, fact := range facts {
		log.Debug().
			Int("index", i+1).
			Int("total", len(facts)).
			Str("fact_text", truncate(fact.NormalizedFactText, 100)).
			Msg("Processing fact for insertion")

		if err := fi.insertSingleFact(ctx, fact); err != nil {
			return fmt.Errorf("failed to insert fact %d/%d: %w", i+1, len(facts), err)
		}
	}
	return nil
}

// insertSingleFact handles insertion of a single fact with similarity detection.
func (fi *FactInserter) insertSingleFact(ctx context.Context, input StoreFactInput) error {
	// SubjectEntity is required - it's the primary person/entity this fact is about
	if input.SubjectEntity == "" {
		return fmt.Errorf("subject_entity is required")
	}

	// Resolve subject entity to person ID, using entity type if provided
	var subjectID int64
	var err error
	if input.SubjectEntityType != "" {
		// Use the new method that accepts entity type
		subjectID, err = fi.storer.FindOrInsertPersonWithType(ctx, input.SubjectEntity, input.SubjectEntityType)
		if err != nil {
			return fmt.Errorf("failed to find/insert subject entity with type: %w", err)
		}
	} else {
		// Fall back to existing method (defaults to human)
		subjectIDs, err := fi.storer.FindOrInsertPersons(ctx, []string{input.SubjectEntity})
		if err != nil {
			return fmt.Errorf("failed to find/insert subject entity: %w", err)
		}
		if len(subjectIDs) == 0 {
			return fmt.Errorf("failed to resolve subject entity: %s", input.SubjectEntity)
		}
		subjectID = subjectIDs[0]
	}

	// Resolve InvolvedPeople as secondary mentions (if any)
	var secondaryPersonIDs []int64
	if len(input.InvolvedPeople) > 0 {
		secondaryPersonIDs, err = fi.storer.FindOrInsertPersons(ctx, input.InvolvedPeople)
		if err != nil {
			log.Warn().Err(err).Strs("involved_people", input.InvolvedPeople).
				Msg("Failed to resolve secondary persons, continuing with subject only")
			secondaryPersonIDs = nil
		}
	}

	// Search for similar canonical facts for the subject entity
	similarFacts, err := fi.storer.FindSimilarCanonicalFacts(
		ctx,
		[]int64{subjectID},
		input.Embedding,
		fi.similarityThreshold,
	)
	if err != nil {
		return fmt.Errorf("failed to find similar facts: %w", err)
	}

	var factID int64

	if len(similarFacts) > 0 {
		// Found similar canonical fact - insert as related fact
		canonicalFactID := similarFacts[0].ID

		log.Info().
			Int64("canonical_fact_id", canonicalFactID).
			Float64("distance", similarFacts[0].Distance).
			Str("canonical_text", truncate(similarFacts[0].Text, 80)).
			Str("new_fact_text", truncate(input.NormalizedFactText, 80)).
			Str("subject_entity", input.SubjectEntity).
			Msg("Found similar canonical fact, inserting as related")

		factID, err = fi.storer.InsertRelatedFact(ctx, input, canonicalFactID)
		if err != nil {
			return fmt.Errorf("failed to insert related fact: %w", err)
		}
		metrics.IncrementFactsExtracted(input.SourceType, false) // Related fact
	} else {
		// No similar fact found - insert as new canonical fact
		log.Info().
			Str("fact_text", truncate(input.NormalizedFactText, 80)).
			Str("subject_entity", input.SubjectEntity).
			Msg("No similar fact found, inserting as canonical")

		factID, err = fi.storer.InsertCanonicalFact(ctx, input)
		if err != nil {
			return fmt.Errorf("failed to insert canonical fact: %w", err)
		}
		metrics.IncrementFactsExtracted(input.SourceType, true) // Canonical fact
	}

	// Link source to the fact (canonical or related)
	sourceID, err := fi.storer.LinkSourceToFact(
		ctx,
		factID,
		input.SourceURL,
		input.OriginalQuote,
		input.PublishedAt,
		input.SourceType,
		input.Metadata,
	)
	if err != nil {
		return fmt.Errorf("failed to link source: %w", err)
	}

	// Link subject entity to this source mention
	if err := fi.storer.LinkPersonsToSourceMention(ctx, sourceID, []int64{subjectID}); err != nil {
		return fmt.Errorf("failed to link subject entity: %w", err)
	}

	// Link secondary persons to this source mention (if any)
	if len(secondaryPersonIDs) > 0 {
		if err := fi.storer.LinkPersonsToSourceMention(ctx, sourceID, secondaryPersonIDs); err != nil {
			log.Warn().Err(err).Interface("secondary_person_ids", secondaryPersonIDs).
				Msg("Failed to link secondary persons to source")
		}
	}

	log.Debug().
		Int64("fact_id", factID).
		Int64("source_id", sourceID).
		Str("subject_entity", input.SubjectEntity).
		Int("num_secondary_persons", len(secondaryPersonIDs)).
		Msg("Successfully inserted fact with source and person links")

	return nil
}

// truncate truncates a string to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
