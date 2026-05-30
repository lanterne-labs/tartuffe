package core

import (
	"context"
	"fmt"
	"os"
	"strconv"

	"github.com/rs/zerolog/log"
)

// DedupeStorer defines the interface for storage operations needed by FactDeduplicator.
// This is typically implemented by storage.PostgresStorer via DedupeStorerAdapter.
type DedupeStorer interface {
	// GetAllPersons retrieves all persons from the database.
	GetAllPersons(ctx context.Context) ([]DedupePersonInfo, error)

	// GetPersonIDByName returns the person ID for a given name.
	GetPersonIDByName(ctx context.Context, name string) (int64, error)

	// GetFactsForDeduplication retrieves facts for a person with their embeddings and text.
	GetFactsForDeduplication(ctx context.Context, personID int64) ([]FactForDedup, error)

	// FindSimilarFactsForDedup finds facts similar to a given embedding within a threshold.
	FindSimilarFactsForDedup(ctx context.Context, personIDs []int64, embedding []float32, excludeFactID int64, threshold float64) ([]SimilarFactResult, error)

	// MarkFactAsDuplicate marks a fact as a duplicate of a canonical fact.
	MarkFactAsDuplicate(ctx context.Context, duplicateID int64, canonicalID int64) error

	// GetFactByID retrieves a single fact by its ID.
	GetFactByID(ctx context.Context, factID int64) (*FactForDedup, error)

	// FindOrInsertPersons resolves person names to person IDs.
	FindOrInsertPersons(ctx context.Context, names []string) ([]int64, error)

	// DeleteFact deletes a fact that has been determined to be invalid.
	DeleteFact(ctx context.Context, factID int64) error
}

// DedupePersonInfo represents basic person information for deduplication.
type DedupePersonInfo struct {
	ID         int64
	Name       string
	EntityType string
}

// FactForDedup represents a fact with fields needed for deduplication.
type FactForDedup struct {
	ID                 int64     `json:"id"`
	NormalizedFactText string    `json:"normalized_fact"`
	CanonicalVerb      string    `json:"canonical_verb,omitempty"`
	EmbeddingText      string    `json:"embedding_text"`
	OriginalQuote      string    `json:"original_quote,omitempty"`
	Embedding          []float32 `json:"-"` // Not included in JSON for LLM
	IsCanonical        bool      `json:"-"`
	CanonicalFactID    *int64    `json:"-"`
}

// SimilarFactResult represents a similar fact found during deduplication.
type SimilarFactResult struct {
	ID         int64
	Text       string
	Distance   float64
	Similarity float64 // Cosine similarity (1 - distance)
}

// FactDeduplicator handles multi-tier fact deduplication.
type FactDeduplicator struct {
	storer      DedupeStorer
	llmVerifier *DedupLLMVerifier
	// Configurable thresholds
	autoDedupeThreshold   float64 // Cosine similarity above which to auto-dedupe (default: 0.92)
	uncertainLowThreshold float64 // Cosine similarity below which to keep separate (default: 0.75)
}

// DedupeOptions configures the deduplication process.
type DedupeOptions struct {
	TargetName string // Optional: only dedupe facts for this person
	DryRun     bool   // Report duplicates without marking them
	SkipLLM    bool   // Only use embedding/text (cheaper, less accurate)
	BatchSize  int    // LLM batch size (default: 50)
}

// DedupeResult contains the outcome of a deduplication run.
type DedupeResult struct {
	TotalFactsChecked int
	AutoDeduped       int // Tier 1 (>0.90 similarity)
	LLMVerified       int // Tier 3 (LLM confirmed duplicate)
	LLMKept           int // Tier 3 (LLM said not duplicate)
	FactsDeleted      int // Invalid facts that were deleted
	DuplicateGroups   []DuplicateGroup
	Deletions         []FactDeletionResult
}

// FactDeletionResult records a fact that was deleted as invalid.
type FactDeletionResult struct {
	ID     int64
	Text   string
	Reason string
}

// DuplicateGroup represents a group of duplicate facts.
type DuplicateGroup struct {
	CanonicalID  int64
	DuplicateIDs []int64
	Reason       string
	Similarity   float64
}

// NewFactDeduplicator creates a new FactDeduplicator with the given storer and optional LLM verifier.
// Thresholds are loaded from environment variables with sensible defaults.
func NewFactDeduplicator(storer DedupeStorer, llmVerifier *DedupLLMVerifier) *FactDeduplicator {
	// Load thresholds from environment or use defaults
	autoDedupeThreshold := getEnvFloat("DEDUPE_AUTO_THRESHOLD", 0.90)
	uncertainLowThreshold := getEnvFloat("DEDUPE_UNCERTAIN_THRESHOLD", 0.75)

	log.Info().
		Float64("auto_threshold", autoDedupeThreshold).
		Float64("uncertain_threshold", uncertainLowThreshold).
		Msg("Initialized FactDeduplicator with thresholds")

	return &FactDeduplicator{
		storer:                storer,
		llmVerifier:           llmVerifier,
		autoDedupeThreshold:   autoDedupeThreshold,
		uncertainLowThreshold: uncertainLowThreshold,
	}
}

// SetThresholds overrides the thresholds loaded from environment variables.
func (d *FactDeduplicator) SetThresholds(autoDedupeThreshold, uncertainLowThreshold float64) {
	d.autoDedupeThreshold = autoDedupeThreshold
	d.uncertainLowThreshold = uncertainLowThreshold
	log.Info().
		Float64("auto_threshold", autoDedupeThreshold).
		Float64("uncertain_threshold", uncertainLowThreshold).
		Msg("Overrode FactDeduplicator thresholds from CLI flags")
}

// DedupeAll runs deduplication on all facts in the database.
func (d *FactDeduplicator) DedupeAll(ctx context.Context, opts DedupeOptions) (*DedupeResult, error) {
	persons, err := d.storer.GetAllPersons(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get all persons: %w", err)
	}

	log.Info().Int("person_count", len(persons)).Msg("Starting deduplication for all persons")

	result := &DedupeResult{}

	for i, person := range persons {
		log.Debug().Int("progress", i+1).Int("total", len(persons)).Str("person", person.Name).Msg("Processing person")

		personResult, err := d.dedupeForPersonID(ctx, person.ID, person.Name, opts)
		if err != nil {
			log.Error().Err(err).Str("person", person.Name).Msg("Failed to dedupe for person, continuing")
			continue
		}

		// Aggregate results
		result.TotalFactsChecked += personResult.TotalFactsChecked
		result.AutoDeduped += personResult.AutoDeduped
		result.LLMVerified += personResult.LLMVerified
		result.LLMKept += personResult.LLMKept
		result.DuplicateGroups = append(result.DuplicateGroups, personResult.DuplicateGroups...)
	}

	return result, nil
}

// DedupeForPerson runs deduplication only for facts associated with a specific person.
func (d *FactDeduplicator) DedupeForPerson(ctx context.Context, personName string, opts DedupeOptions) (*DedupeResult, error) {
	personID, err := d.storer.GetPersonIDByName(ctx, personName)
	if err != nil {
		return nil, fmt.Errorf("failed to find person %q: %w", personName, err)
	}

	return d.dedupeForPersonID(ctx, personID, personName, opts)
}

// dedupeForPersonID performs deduplication for a specific person by ID.
func (d *FactDeduplicator) dedupeForPersonID(ctx context.Context, personID int64, personName string, opts DedupeOptions) (*DedupeResult, error) {
	facts, err := d.storer.GetFactsForDeduplication(ctx, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to get facts for person %d: %w", personID, err)
	}

	if len(facts) < 2 {
		log.Debug().Str("person", personName).Int("facts", len(facts)).Msg("Not enough facts to dedupe")
		return &DedupeResult{TotalFactsChecked: len(facts)}, nil
	}

	log.Info().Str("person", personName).Int("facts", len(facts)).Msg("Processing facts for deduplication")

	result := &DedupeResult{TotalFactsChecked: len(facts)}

	// Track which facts are already marked as duplicates to skip them
	processedFacts := make(map[int64]bool)

	// Collect uncertain pairs for LLM verification
	var uncertainPairs []uncertainPair

	// For each canonical fact, find potential duplicates
	for i, fact := range facts {
		// Skip non-canonical facts
		if !fact.IsCanonical {
			continue
		}

		// Skip already processed facts
		if processedFacts[fact.ID] {
			continue
		}

		// Find similar facts
		personIDs := []int64{personID}
		// Convert auto-dedupe threshold to cosine distance (1 - similarity)
		distanceThreshold := 1.0 - d.uncertainLowThreshold

		similarFacts, err := d.storer.FindSimilarFactsForDedup(ctx, personIDs, fact.Embedding, fact.ID, distanceThreshold)
		if err != nil {
			log.Error().Err(err).Int64("fact_id", fact.ID).Msg("Failed to find similar facts")
			continue
		}

		for _, similar := range similarFacts {
			// Skip non-canonical facts (already duplicates)
			similarFact, err := d.storer.GetFactByID(ctx, similar.ID)
			if err != nil {
				log.Error().Err(err).Int64("fact_id", similar.ID).Msg("Failed to get similar fact")
				continue
			}
			if !similarFact.IsCanonical {
				continue
			}

			// Skip already processed
			if processedFacts[similar.ID] {
				continue
			}

			// Calculate cosine similarity from distance
			similarity := similar.Similarity
			if similarity == 0 {
				similarity = 1.0 - similar.Distance
			}

			// Tier 1: Auto-dedupe for high similarity
			// Verb-aware: only dedup if same verb OR both external (non-direct-claim).
			// Different direct-claim verbs (DENIED vs CLAIMED) must coexist.
			if similarity >= d.autoDedupeThreshold {
				sameVerb := fact.CanonicalVerb == similarFact.CanonicalVerb
				bothExternal := !isDirectClaimVerb(fact.CanonicalVerb) && !isDirectClaimVerb(similarFact.CanonicalVerb)
				if !sameVerb && !bothExternal {
					log.Debug().
						Int64("fact_a", fact.ID).
						Int64("fact_b", similar.ID).
						Str("verb_a", fact.CanonicalVerb).
						Str("verb_b", similarFact.CanonicalVerb).
						Float64("similarity", similarity).
						Msg("High similarity but different direct-claim verbs, NOT deduping")
					continue
				}

				log.Info().
					Int64("canonical", fact.ID).
					Int64("duplicate", similar.ID).
					Float64("similarity", similarity).
					Bool("both_external", bothExternal).
					Msg("Auto-deduping high-similarity fact")

				if !opts.DryRun {
					if err := d.storer.MarkFactAsDuplicate(ctx, similar.ID, fact.ID); err != nil {
						log.Error().Err(err).Msg("Failed to mark fact as duplicate")
						continue
					}
				}

				processedFacts[similar.ID] = true
				result.AutoDeduped++
				result.DuplicateGroups = append(result.DuplicateGroups, DuplicateGroup{
					CanonicalID:  fact.ID,
					DuplicateIDs: []int64{similar.ID},
					Reason:       fmt.Sprintf("Auto-dedupe: %.2f%% similar", similarity*100),
					Similarity:   similarity,
				})
				continue
			}

			// Uncertain zone (75-92%): queue for LLM verification
			if similarity >= d.uncertainLowThreshold {
				uncertainPairs = append(uncertainPairs, uncertainPair{
					factA:      fact,
					factB:      *similarFact,
					similarity: similarity,
				})
			}
		}

		// Progress logging
		if (i+1)%50 == 0 {
			log.Debug().Int("progress", i+1).Int("total", len(facts)).Msg("Deduplication progress")
		}
	}

	// Process uncertain pairs with LLM (if enabled)
	if len(uncertainPairs) > 0 && !opts.SkipLLM && d.llmVerifier != nil {
		llmResult, err := d.processUncertainPairs(ctx, uncertainPairs, opts)
		if err != nil {
			log.Error().Err(err).Msg("Failed to process uncertain pairs with LLM")
		} else {
			result.LLMVerified += llmResult.verified
			result.LLMKept += llmResult.kept
			result.FactsDeleted += llmResult.deleted
			result.DuplicateGroups = append(result.DuplicateGroups, llmResult.groups...)
			result.Deletions = append(result.Deletions, llmResult.deletions...)
		}
	} else if len(uncertainPairs) > 0 && opts.SkipLLM {
		log.Info().Int("uncertain_pairs", len(uncertainPairs)).Msg("Skipping LLM verification (--skip-llm)")
	}

	return result, nil
}

// uncertainPair holds a pair of facts in the uncertain similarity zone.
type uncertainPair struct {
	factA      FactForDedup
	factB      FactForDedup
	similarity float64
}

// llmProcessResult contains results from LLM verification.
type llmProcessResult struct {
	verified  int
	kept      int
	deleted   int
	groups    []DuplicateGroup
	deletions []FactDeletionResult
}

// processUncertainPairs processes uncertain pairs using LLM verification.
func (d *FactDeduplicator) processUncertainPairs(ctx context.Context, pairs []uncertainPair, opts DedupeOptions) (*llmProcessResult, error) {
	result := &llmProcessResult{}

	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = getEnvInt("DEDUPE_LLM_BATCH_SIZE", 10)
	}

	log.Info().Int("pairs", len(pairs)).Int("batch_size", batchSize).Msg("Processing uncertain pairs with LLM")

	// Process in batches
	for i := 0; i < len(pairs); i += batchSize {
		end := i + batchSize
		if end > len(pairs) {
			end = len(pairs)
		}
		batch := pairs[i:end]

		// Build facts list for LLM
		var factsForLLM []FactForDedup
		factMap := make(map[int64]FactForDedup)

		for _, pair := range batch {
			if _, exists := factMap[pair.factA.ID]; !exists {
				factMap[pair.factA.ID] = pair.factA
				factsForLLM = append(factsForLLM, pair.factA)
			}
			if _, exists := factMap[pair.factB.ID]; !exists {
				factMap[pair.factB.ID] = pair.factB
				factsForLLM = append(factsForLLM, pair.factB)
			}
		}

		// Call LLM verifier
		llmResponse, err := d.llmVerifier.VerifyBatch(ctx, factsForLLM)
		if err != nil {
			log.Error().Err(err).Int("batch_start", i).Msg("LLM verification failed for batch")
			continue
		}

		// Process LLM response
		for _, group := range llmResponse.UniqueGroups {
			if len(group.DuplicateIDs) == 0 {
				continue
			}

			// Mark duplicates
			if !opts.DryRun {
				for _, dupID := range group.DuplicateIDs {
					if err := d.storer.MarkFactAsDuplicate(ctx, dupID, group.KeepID); err != nil {
						log.Error().Err(err).Int64("duplicate", dupID).Int64("canonical", group.KeepID).Msg("Failed to mark duplicate")
						continue
					}
				}
			}

			result.verified += len(group.DuplicateIDs)
			result.groups = append(result.groups, DuplicateGroup{
				CanonicalID:  group.KeepID,
				DuplicateIDs: group.DuplicateIDs,
				Reason:       group.Reason,
				Similarity:   0, // LLM-verified, similarity varies
			})
		}

		// Process invalid facts (delete them)
		for _, invalid := range llmResponse.InvalidFacts {
			fact, exists := factMap[invalid.ID]
			if !exists {
				continue
			}

			log.Warn().
				Int64("fact_id", invalid.ID).
				Str("reason", invalid.Reason).
				Msg("LLM flagged fact as invalid")

			if !opts.DryRun {
				if err := d.storer.DeleteFact(ctx, invalid.ID); err != nil {
					log.Error().Err(err).Int64("fact_id", invalid.ID).Msg("Failed to delete invalid fact")
					continue
				}
			}

			result.deleted++
			result.deletions = append(result.deletions, FactDeletionResult{
				ID:     invalid.ID,
				Text:   fact.NormalizedFactText,
				Reason: invalid.Reason,
			})
		}

		// Count facts kept separate (not in any group, not deleted)
		processedIDs := make(map[int64]bool)
		for _, group := range llmResponse.UniqueGroups {
			processedIDs[group.KeepID] = true
			for _, id := range group.DuplicateIDs {
				processedIDs[id] = true
			}
		}
		for _, invalid := range llmResponse.InvalidFacts {
			processedIDs[invalid.ID] = true
		}
		for _, fact := range factsForLLM {
			if !processedIDs[fact.ID] {
				result.kept++
			}
		}
	}

	return result, nil
}

// Helper functions for loading environment variables

func getEnvFloat(key string, defaultVal float64) float64 {
	if val := os.Getenv(key); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return defaultVal
}
