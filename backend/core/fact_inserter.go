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

// FactRelationInput represents a relation between two facts.
// This type is defined in core to avoid import cycles between core and storage.
type FactRelationInput struct {
	GeneralFactID  int64
	SpecificFactID int64
	RelationType   string // "duplicate" or "implication"
	Reason         string
}

// InsertedFactMeta holds metadata about a successfully inserted fact.
// Used by batch processing to chain classification after fact extraction.
type InsertedFactMeta struct {
	FactID   int64
	PersonID int64 // Resolved subject entity person ID
}

// InsertResult contains the outcome of a batch InsertFacts call.
type InsertResult struct {
	Inserted     int // Number of facts successfully inserted
	Skipped      int // Number of facts skipped as duplicates
	InsertedMeta []InsertedFactMeta // Metadata for each successfully inserted fact
}

// FactInserter handles intelligent fact insertion with relation classification.
// It always inserts facts, then classifies their relationships to existing similar facts
// (duplicate, implication, or unrelated) via an LLM call.
type FactInserter struct {
	storer               Storer
	classifier           *RelationClassifier
	stanceManager        *StanceManager
	similarityThreshold  float64 // Cosine distance threshold for relation candidate search
	autoDedupeThreshold  float64 // Cosine similarity above which to skip as duplicate
	skipClassification   bool    // When true, skip relation classification entirely (for batch contexts)
}

// Storer defines the interface for storage operations needed by FactInserter.
// This is typically implemented by storage.PostgresStorer.
type Storer interface {
	// FindOrInsertPersons resolves person names to person IDs, creating new records if needed.
	FindOrInsertPersons(ctx context.Context, names []string) ([]int64, error)

	// FindOrInsertPersonWithType resolves a person name to a person ID with entity type.
	FindOrInsertPersonWithType(ctx context.Context, name string, entityType string) (int64, error)

	// FindSimilarActiveFacts searches for active (non-duplicate) facts similar to the given embedding.
	FindSimilarActiveFacts(ctx context.Context, personIDs []int64, embedding []float32, threshold float64, limit int) ([]SimilarFact, error)

	// InsertFact inserts a new fact into the database.
	InsertFact(ctx context.Context, input StoreFactInput) (factID int64, err error)

	// InsertFactRelationsBatch inserts multiple fact relations efficiently.
	InsertFactRelationsBatch(ctx context.Context, inputs []FactRelationInput) error

	// IsAncestorOf checks if ancestorID is an ancestor of descendantID via implication chain.
	IsAncestorOf(ctx context.Context, ancestorID int64, descendantID int64) (bool, error)

	// LinkSourceToFact links a source URL to a fact, creating a fact_sources record.
	LinkSourceToFact(ctx context.Context, factID int64, sourceURL string, originalQuote string, publishedAt *time.Time, sourceType string, metadata map[string]any) (sourceID int64, err error)

	// LinkPersonsToSourceMention links persons to a fact source, creating fact_mentions records.
	LinkPersonsToSourceMention(ctx context.Context, sourceID int64, personIDs []int64) error

	// GetFactCanonicalVerb returns the canonical verb for a given fact ID.
	GetFactCanonicalVerb(ctx context.Context, factID int64) (string, error)
}

// StoreFactInput encapsulates the data needed to store a fact and its source.
type StoreFactInput struct {
	SubjectEntity      string    // Primary entity this fact is about (resolved to person ID for linking)
	SubjectEntityType  string    // Entity type: "human", "party", or "organization" (used when creating new persons)
	NormalizedFactText string    // Full fact text with names (for display and LLM comparison)
	EmbeddingText      string    // Name-free version used ONLY for embedding generation
	Embedding          []float32 // Vector embedding (generated from EmbeddingText)
	OriginalQuote      string
	CanonicalVerb      string
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
// The relation classifier is self-initialized from OPEN_AI_* env vars.
// If env vars are missing (tests, no LLM configured), the classifier is nil (graceful degradation).
// The similarity threshold for relation candidate search is loaded from IMPLICATION_SIMILARITY_THRESHOLD
// env var, defaulting to 0.60 (60% cosine similarity).
func NewFactInserter(storer Storer) *FactInserter {
	var classifier *RelationClassifier
	c, err := NewRelationClassifierFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("Relation classifier not available, skipping relation classification")
	} else {
		classifier = c
	}
	cosineSimilarity := 0.60 // default: 60% similar (broader search for relation candidates)
	if env := os.Getenv("IMPLICATION_SIMILARITY_THRESHOLD"); env != "" {
		if t, err := strconv.ParseFloat(env, 64); err == nil {
			cosineSimilarity = t
			log.Info().Float64("cosine_similarity", cosineSimilarity).Msg("Loaded IMPLICATION_SIMILARITY_THRESHOLD from environment")
		} else {
			log.Warn().Str("value", env).Err(err).Msg("Invalid IMPLICATION_SIMILARITY_THRESHOLD, using default 0.60")
		}
	}

	cosineDistance := 1.0 - cosineSimilarity
	autoDedupeThreshold := getEnvFloat("DEDUPE_AUTO_THRESHOLD", 0.85)

	return &FactInserter{
		storer:              storer,
		classifier:          classifier,
		similarityThreshold: cosineDistance,
		autoDedupeThreshold: autoDedupeThreshold,
	}
}

// SetStanceManager sets the optional stance manager for stance-based contradiction detection.
func (fi *FactInserter) SetStanceManager(sm *StanceManager) {
	fi.stanceManager = sm
}

// DisableClassifier disables the relation classifier for this inserter.
// Use this for batch contexts where sync LLM calls per fact are wasteful —
// contradiction detection is handled separately via batch jobs.
func (fi *FactInserter) DisableClassifier() {
	fi.skipClassification = true
}

// InsertFacts processes a batch of facts, inserting each and classifying relations.
// Facts that are near-duplicates of existing active facts (above autoDedupeThreshold) are skipped.
func (fi *FactInserter) InsertFacts(ctx context.Context, facts []StoreFactInput) (*InsertResult, error) {
	result := &InsertResult{}
	for i, fact := range facts {
		log.Debug().
			Int("index", i+1).
			Int("total", len(facts)).
			Str("fact_text", truncate(fact.NormalizedFactText, 100)).
			Msg("Processing fact for insertion")

		factID, personID, inserted, err := fi.insertSingleFact(ctx, fact)
		if err != nil {
			return result, fmt.Errorf("failed to insert fact %d/%d: %w", i+1, len(facts), err)
		}
		if inserted {
			result.Inserted++
			result.InsertedMeta = append(result.InsertedMeta, InsertedFactMeta{
				FactID:   factID,
				PersonID: personID,
			})
		} else {
			result.Skipped++
		}
	}
	return result, nil
}

// insertSingleFact handles insertion of a single fact with relation classification.
// Returns (factID, personID, inserted, error). factID/personID are 0 when skipped.
func (fi *FactInserter) insertSingleFact(ctx context.Context, input StoreFactInput) (int64, int64, bool, error) {
	if input.SubjectEntity == "" {
		return 0, 0, false, fmt.Errorf("subject_entity is required")
	}

	// Resolve subject entity to person ID
	var subjectID int64
	var err error
	if input.SubjectEntityType != "" {
		subjectID, err = fi.storer.FindOrInsertPersonWithType(ctx, input.SubjectEntity, input.SubjectEntityType)
		if err != nil {
			return 0, 0, false, fmt.Errorf("failed to find/insert subject entity with type: %w", err)
		}
	} else {
		subjectIDs, err := fi.storer.FindOrInsertPersons(ctx, []string{input.SubjectEntity})
		if err != nil {
			return 0, 0, false, fmt.Errorf("failed to find/insert subject entity: %w", err)
		}
		if len(subjectIDs) == 0 {
			return 0, 0, false, fmt.Errorf("failed to resolve subject entity: %s", input.SubjectEntity)
		}
		subjectID = subjectIDs[0]
	}

	// Verb-aware near-duplicate check before inserting
	// Same verb + high similarity → auto-dedup (skip)
	// Different verb → NEVER auto-dedup (opposing stances on same topic must coexist)
	if len(input.Embedding) > 0 {
		distanceThreshold := 1.0 - fi.autoDedupeThreshold
		similar, err := fi.storer.FindSimilarActiveFacts(ctx, []int64{subjectID}, input.Embedding, distanceThreshold, 5)
		if err != nil {
			log.Warn().Err(err).Str("fact", truncate(input.NormalizedFactText, 80)).
				Msg("Dedup check failed, proceeding with insertion")
		} else {
			for _, sf := range similar {
				existingVerb, verbErr := fi.storer.GetFactCanonicalVerb(ctx, sf.ID)
				if verbErr != nil {
					log.Debug().Err(verbErr).Int64("fact_id", sf.ID).Msg("Failed to get canonical verb for similar fact")
					continue
				}
				// Dedup if same verb, OR if both are external (non-direct-claim) verbs.
				// Direct claim verbs (DENIED, CLAIMED, etc.) with different verbs must coexist
				// to preserve opposing stances. But external verbs (WAS_SENTENCED, WAS_CONVICTED, etc.)
				// often describe the same event with different wording.
				bothExternal := !isDirectClaimVerb(existingVerb) && !isDirectClaimVerb(input.CanonicalVerb)
				if existingVerb == input.CanonicalVerb || bothExternal {
					log.Info().
						Int64("existing_fact_id", sf.ID).
						Float64("distance", sf.Distance).
						Str("new_verb", input.CanonicalVerb).
						Str("existing_verb", existingVerb).
						Bool("both_external", bothExternal).
						Str("fact", truncate(input.NormalizedFactText, 80)).
						Msg("Skipping duplicate fact (same verb or both external + high similarity)")
					return 0, 0, false, nil
				}
				log.Debug().
					Int64("existing_fact_id", sf.ID).
					Float64("distance", sf.Distance).
					Str("new_verb", input.CanonicalVerb).
					Str("existing_verb", existingVerb).
					Msg("High similarity but different direct-claim verbs, NOT deduping")
			}
		}
	}

	// Insert the fact
	factID, err := fi.storer.InsertFact(ctx, input)
	if err != nil {
		return 0, 0, false, fmt.Errorf("failed to insert fact: %w", err)
	}
	metrics.IncrementFactsExtracted(input.SourceType, true)

	// Link source to the fact
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
		return 0, 0, false, fmt.Errorf("failed to link source: %w", err)
	}

	// Link subject entity to this source mention
	if err := fi.storer.LinkPersonsToSourceMention(ctx, sourceID, []int64{subjectID}); err != nil {
		return 0, 0, false, fmt.Errorf("failed to link subject entity: %w", err)
	}

	// Stance-based processing (replaces classifyRelations when enabled)
	if fi.stanceManager != nil {
		result, err := fi.stanceManager.ProcessNewFact(ctx, subjectID, factID, input.NormalizedFactText, input.Embedding, input.CanonicalVerb)
		if err != nil {
			log.Warn().Err(err).Int64("fact_id", factID).Msg("Stance processing failed, falling back to relation classification")
			// Fall through to classifyRelations below
		} else {
			log.Info().
				Int64("fact_id", factID).
				Str("verdict", result.Verdict).
				Int64("stance_id", result.StanceID).
				Msg("Stance processing complete")
			// Skip classifyRelations - stance manager handles contradiction detection
			return factID, subjectID, true, nil
		}
	}

	// Classify relations with similar facts (non-blocking - fact is already inserted)
	if fi.classifier != nil && !fi.skipClassification {
		fi.classifyRelations(ctx, factID, input, subjectID)
	}

	log.Debug().
		Int64("fact_id", factID).
		Int64("source_id", sourceID).
		Str("subject_entity", input.SubjectEntity).
		Msg("Successfully inserted fact with source and person links")

	return factID, subjectID, true, nil
}

// classifyRelations finds similar active facts and classifies their relationships.
// This is non-blocking: errors are logged but don't prevent fact insertion.
func (fi *FactInserter) classifyRelations(ctx context.Context, newFactID int64, input StoreFactInput, subjectID int64) {
	similarFacts, err := fi.storer.FindSimilarActiveFacts(
		ctx,
		[]int64{subjectID},
		input.Embedding,
		fi.similarityThreshold,
		20,
	)
	if err != nil {
		log.Warn().Err(err).Int64("fact_id", newFactID).Msg("Failed to find similar facts for relation classification")
		return
	}

	if len(similarFacts) == 0 {
		return
	}

	// Build classification inputs (exclude the new fact itself)
	var classInputs []ClassificationInput
	for _, sf := range similarFacts {
		if sf.ID == newFactID {
			continue
		}
		existingVerb, _ := fi.storer.GetFactCanonicalVerb(ctx, sf.ID)
		classInputs = append(classInputs, ClassificationInput{
			ExistingFactID:   sf.ID,
			ExistingFactText: sf.Text,
			CanonicalVerb:    existingVerb,
			// PublishedAt not available from SimilarFact; nil is acceptable
		})
	}

	if len(classInputs) == 0 {
		return
	}

	results, err := fi.classifier.Classify(ctx, input.CanonicalVerb, input.NormalizedFactText, input.PublishedAt, classInputs)
	if err != nil {
		log.Warn().Err(err).Int64("fact_id", newFactID).Msg("Relation classification failed, fact already inserted")
		return
	}

	if len(results) == 0 {
		return
	}

	// Convert classification results to fact relation inputs
	var relations []FactRelationInput
	for _, r := range results {
		var rel FactRelationInput
		rel.Reason = r.Reason

		switch r.Relation {
		case "duplicate":
			// For duplicates: the older fact is general, the newer is specific
			// Since we just inserted newFact, it's the newer one
			rel.GeneralFactID = r.ExistingFactID
			rel.SpecificFactID = newFactID
			rel.RelationType = "duplicate"

		case "implication_new_is_general":
			// New fact is more general, existing is more specific
			rel.GeneralFactID = newFactID
			rel.SpecificFactID = r.ExistingFactID
			rel.RelationType = "implication"

			// Cycle prevention: check that existing fact is not already an ancestor of new fact
			isAnc, err := fi.storer.IsAncestorOf(ctx, r.ExistingFactID, newFactID)
			if err != nil {
				log.Warn().Err(err).Int64("fact_id", newFactID).Int64("existing", r.ExistingFactID).
					Msg("Failed to check ancestry, skipping relation")
				continue
			}
			if isAnc {
				log.Warn().Int64("fact_id", newFactID).Int64("existing", r.ExistingFactID).
					Msg("Skipping implication relation: would create cycle")
				continue
			}

		case "implication_new_is_specific":
			// New fact is more specific, existing is more general
			rel.GeneralFactID = r.ExistingFactID
			rel.SpecificFactID = newFactID
			rel.RelationType = "implication"

			// Cycle prevention: check that new fact is not already an ancestor of existing
			isAnc, err := fi.storer.IsAncestorOf(ctx, newFactID, r.ExistingFactID)
			if err != nil {
				log.Warn().Err(err).Int64("fact_id", newFactID).Int64("existing", r.ExistingFactID).
					Msg("Failed to check ancestry, skipping relation")
				continue
			}
			if isAnc {
				log.Warn().Int64("fact_id", newFactID).Int64("existing", r.ExistingFactID).
					Msg("Skipping implication relation: would create cycle")
				continue
			}

		default:
			continue
		}

		relations = append(relations, rel)
	}

	if len(relations) > 0 {
		if err := fi.storer.InsertFactRelationsBatch(ctx, relations); err != nil {
			log.Warn().Err(err).Int64("fact_id", newFactID).Msg("Failed to insert fact relations")
		} else {
			log.Info().Int64("fact_id", newFactID).Int("relations", len(relations)).Msg("Recorded fact relations")
		}
	}
}

// truncate truncates a string to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// isDirectClaimVerb returns true if the verb represents a direct claim or action
// by the person (as opposed to an external attribution like a court ruling).
// Duplicated from categorizer.IsDirectClaimVerb to avoid import cycle.
func isDirectClaimVerb(verb string) bool {
	switch verb {
	case "DENIED", "CLAIMED", "PROMISED", "SUPPORTED",
		"OPPOSED", "VOTED_FOR", "VOTED_AGAINST", "PROPOSED",
		"CONFIRMED", "ADMITTED", "REFUSED", "DEMANDED",
		"THREATENED", "ANNOUNCED", "CHANGED_POSITION":
		return true
	default:
		return false
	}
}
