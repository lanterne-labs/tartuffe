package core

import (
	"context"
	"digital-mirror/backend/metrics"
	"fmt"
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
	Inserted     int                 // Number of facts successfully inserted
	Skipped      int                 // Number of facts skipped as duplicates
	InsertedMeta []InsertedFactMeta // Metadata for each successfully inserted fact
}

// StanceProcessor routes a newly inserted fact to its (existing or new) stance.
// The production implementation is *StanceManager (LLM-backed); tests may
// supply a stub that satisfies the invariant without an LLM round-trip.
type StanceProcessor interface {
	ProcessNewFact(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, verb string) (*StanceProcessResult, error)
}

// FactInserter handles fact insertion with mandatory stance-based processing.
// Every inserted fact is routed through StanceProcessor.ProcessNewFact, which
// classifies it against existing stances (DUPLICATE / NEW_INFO / CONTRADICTION /
// NEW_STANCE) and updates the per-person stance documents accordingly. This is
// the single ingestion path: there is no fallback, no skip flag, and no other
// way to land a fact in the database.
type FactInserter struct {
	storer              Storer
	stanceProcessor     StanceProcessor
	autoDedupeThreshold float64 // Cosine similarity above which to skip as duplicate
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

	// DeleteFact removes a fact and its dependent rows. Used as a compensating
	// action when stance processing fails after the fact was already written.
	DeleteFact(ctx context.Context, factID int64) error

	// LinkSourceToFact links a source URL to a fact, creating a fact_sources record.
	LinkSourceToFact(ctx context.Context, factID int64, sourceURL string, originalQuote string, publishedAt *time.Time, sourceType string, metadata map[string]any) (sourceID int64, err error)

	// LinkPersonsToSourceMention links persons to a fact source, creating fact_mentions records.
	LinkPersonsToSourceMention(ctx context.Context, sourceID int64, personIDs []int64) error

	// GetFactCanonicalVerb returns the canonical verb for a given fact ID.
	GetFactCanonicalVerb(ctx context.Context, factID int64) (string, error)

	// InsertFactRelationsBatch inserts duplicate/implication relations. Still used
	// by the OpenAI-batch classification flow in batch_job.go; that path is now
	// redundant with stance-based clustering and queued for removal.
	InsertFactRelationsBatch(ctx context.Context, inputs []FactRelationInput) error
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

// NewFactInserter creates a new FactInserter. The StanceProcessor is REQUIRED:
// every fact must be routed through stance processing so that the
// "every fact has a stance" invariant holds for the whole codebase. Callers
// without a StanceProcessor cannot insert facts — this is intentional.
func NewFactInserter(storer Storer, stance StanceProcessor) *FactInserter {
	if storer == nil {
		panic("NewFactInserter: storer is nil")
	}
	if stance == nil {
		panic("NewFactInserter: stance processor is nil — every fact must be routed through a stance, no exceptions")
	}

	autoDedupeThreshold := getEnvFloat("DEDUPE_AUTO_THRESHOLD", 0.85)

	return &FactInserter{
		storer:              storer,
		stanceProcessor:     stance,
		autoDedupeThreshold: autoDedupeThreshold,
	}
}

// InsertFacts processes a batch of facts. Each fact goes through verb-aware
// near-duplicate dedup, then is inserted, then is routed through the stance
// manager. If stance processing fails for any fact, the fact (and its source/
// mention rows) is deleted and the batch fails — there is no silent fallback.
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

// insertSingleFact handles insertion of a single fact and its mandatory stance
// routing. Returns (factID, personID, inserted, error). factID/personID are 0
// when the fact was skipped as a near-duplicate.
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

	// Verb-aware near-duplicate check before inserting.
	// Same verb (or both external verbs) + high similarity → skip.
	// Different direct-claim verbs MUST coexist to preserve opposing stances.
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
		fi.compensateDelete(ctx, factID, "linking source")
		return 0, 0, false, fmt.Errorf("failed to link source: %w", err)
	}

	// Link subject entity to this source mention
	if err := fi.storer.LinkPersonsToSourceMention(ctx, sourceID, []int64{subjectID}); err != nil {
		fi.compensateDelete(ctx, factID, "linking subject entity")
		return 0, 0, false, fmt.Errorf("failed to link subject entity: %w", err)
	}

	// Mandatory stance processing. Failure rolls back the fact via compensating
	// delete so that the "every fact has a stance" invariant holds.
	stanceResult, err := fi.stanceProcessor.ProcessNewFact(ctx, subjectID, factID, input.NormalizedFactText, input.Embedding, input.CanonicalVerb)
	if err != nil {
		fi.compensateDelete(ctx, factID, "stance processing")
		return 0, 0, false, fmt.Errorf("stance processing failed for fact %d: %w", factID, err)
	}

	log.Info().
		Int64("fact_id", factID).
		Int64("source_id", sourceID).
		Str("subject_entity", input.SubjectEntity).
		Str("verdict", stanceResult.Verdict).
		Int64("stance_id", stanceResult.StanceID).
		Msg("Inserted fact and routed through stance")

	return factID, subjectID, true, nil
}

// compensateDelete rolls back a partially inserted fact. Errors here are logged
// but cannot be propagated (we are already in the error path). A separate audit
// metric flags any orphan facts that slip through.
func (fi *FactInserter) compensateDelete(ctx context.Context, factID int64, stage string) {
	if delErr := fi.storer.DeleteFact(ctx, factID); delErr != nil {
		log.Error().
			Err(delErr).
			Int64("fact_id", factID).
			Str("stage", stage).
			Msg("Compensating delete failed — fact may be orphaned without a stance")
	} else {
		log.Warn().
			Int64("fact_id", factID).
			Str("stage", stage).
			Msg("Rolled back fact after failure during ingestion")
	}
}

// truncate truncates a string to maxLen runes, appending "..." if truncated.
// Operates on runes (not bytes) so it never splits a multi-byte UTF-8 codepoint
// — Postgres rejects invalid UTF-8 with SQLSTATE 22021.
func truncate(s string, maxLen int) string {
	r := []rune(s)
	if len(r) <= maxLen {
		return s
	}
	return string(r[:maxLen]) + "..."
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
