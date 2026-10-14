package frpolitics

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// FactRecord captures a single fact to be stored using storage layer
type FactRecord struct {
	// PersonName is the normalized person name to mention (human or party)
	PersonName string
	// EntityType is PersonName's entity type: "human" for a deputy, "party"
	// for a group
	EntityType string
	// FactText is the normalized fact text, starting with PersonName
	FactText string
	// EmbeddingText is the name-free law/object phrase used for the embedding:
	// every ballot on one scrutin shares it
	EmbeddingText string
	// CanonicalVerb is the canonical verb (VOTED_FOR, VOTED_AGAINST, etc.)
	CanonicalVerb string
	// SourceURL is the canonical law page URL
	SourceURL string
	// LawKey is a minimal identifier recorded in fact source metadata
	LawKey string
	// SeanceRef is the séance the vote was held in (see Law.SeanceRef)
	SeanceRef string
	// LawTitle is the full legal title of the law
	LawTitle string
	// LawShortTitle is the AI-derived short title
	LawShortTitle string
	// LawSummary is the AI-derived summary
	LawSummary string
	// PublishedAt is the vote datetime if known
	PublishedAt *time.Time
}

// StoreFacts stores the ballots of one scrutin: one fact per record, its
// subject being the record's PersonName. Embeddings are generated once per
// unique EmbeddingText and shared by every ballot on the same scrutin. The
// subject's fact_count and score are refreshed by the mention insert, as for
// any other fact.
func StoreFacts(ctx context.Context, storer *storage.PostgresStorer, records []FactRecord) error {
	if len(records) == 0 {
		return nil
	}

	embGen, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize embedding generator: %w", err)
	}

	seen := make(map[string]bool)
	var embeddingTexts []string
	for _, rec := range records {
		if rec.EmbeddingText == "" {
			return fmt.Errorf("vote fact %q (law %s) has no embedding text", rec.FactText, rec.LawKey)
		}
		if !seen[rec.EmbeddingText] {
			seen[rec.EmbeddingText] = true
			embeddingTexts = append(embeddingTexts, rec.EmbeddingText)
		}
	}

	// Generate embeddings in batches
	batchSize := 50
	embeddingsMap := make(map[string][]float32, len(embeddingTexts))
	totalBatches := (len(embeddingTexts) + batchSize - 1) / batchSize

	for i := 0; i < len(embeddingTexts); i += batchSize {
		end := i + batchSize
		if end > len(embeddingTexts) {
			end = len(embeddingTexts)
		}
		currentBatch := (i / batchSize) + 1

		// Log progress every 10% of batches or every batch if less than 10 batches
		if totalBatches <= 10 || currentBatch%(totalBatches/10) == 0 || currentBatch == 1 || currentBatch == totalBatches {
			log.Info().
				Int("current_batch", currentBatch).
				Int("total_batches", totalBatches).
				Int("percent", (currentBatch*100)/totalBatches).
				Int("facts_start", i+1).
				Int("facts_end", end).
				Msg("frpolitics: embedding generation progress")
		}

		batchTexts := embeddingTexts[i:end]
		embs, err := embGen.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, batchTexts)
		if err != nil {
			return fmt.Errorf("failed to generate embeddings for batch %d-%d: %w", i, end, err)
		}

		for j, text := range batchTexts {
			embeddingsMap[text] = embs[j]
		}

		// Add small delay between batches to avoid rate limiting (skip on last batch)
		if currentBatch < totalBatches {
			time.Sleep(200 * time.Millisecond)
		}
	}

	stanceMgr, err := core.NewStanceManagerFromEnv(storer)
	if err != nil {
		return fmt.Errorf("stance manager required for fact ingestion: %w", err)
	}
	return storeBallots(ctx, storer, stanceMgr, records, embeddingsMap)
}

// storeBallots inserts the ballots of one scrutin and places each one in a
// stance of its voter (votePlacer). Every record must have the voted object
// of the scrutin as its EmbeddingText.
func storeBallots(ctx context.Context, storer *storage.PostgresStorer, stancer voteStancer, records []FactRecord, embeddings map[string][]float32) error {
	objectPhrase := records[0].EmbeddingText
	for _, rec := range records {
		if rec.EmbeddingText != objectPhrase {
			return fmt.Errorf("ballots of more than one scrutin: %q (law %s) and %q (law %s)", objectPhrase, records[0].LawKey, rec.EmbeddingText, rec.LawKey)
		}
	}
	placer, err := newVotePlacer(ctx, NewStore(storer), stancer, records[0])
	if err != nil {
		return err
	}
	inputs, err := buildVoteFactInputs(records, embeddings, placer.subject)
	if err != nil {
		return err
	}

	inserter := core.NewFactInserter(storer, placer)
	inserter.EnableDedupe(storer)

	log.Info().
		Int("total_facts", len(inputs)).
		Str("vote_subject", placer.subject).
		Int("voters_with_subject_stance", len(placer.known)).
		Msg("frpolitics: starting fact insertion")

	if _, err := inserter.InsertFacts(ctx, inputs); err != nil {
		return fmt.Errorf("failed to insert facts: %w", err)
	}

	log.Info().
		Int("total_facts", len(inputs)).
		Msg("frpolitics: fact insertion complete")
	return nil
}

// buildVoteFactInputs turns each record into one inserter input whose subject
// is the record's PersonName, attaching the embedding of its EmbeddingText and
// the vote subject of the scrutin (metadata vote_subject).
func buildVoteFactInputs(records []FactRecord, embeddings map[string][]float32, voteSubject string) ([]core.StoreFactInput, error) {
	inputs := make([]core.StoreFactInput, 0, len(records))
	for _, rec := range records {
		emb, ok := embeddings[rec.EmbeddingText]
		if !ok {
			return nil, fmt.Errorf("no embedding for %q (law %s, %s)", rec.EmbeddingText, rec.LawKey, rec.PersonName)
		}

		// Ensure metadata includes law information for traceability
		metadata := map[string]any{
			"law_key":         rec.LawKey,
			"law_title":       rec.LawTitle,
			"law_short_title": rec.LawShortTitle,
			"law_summary":     rec.LawSummary,
			"vote_subject":    voteSubject,
		}
		if rec.SourceURL != "" {
			metadata["source_url"] = rec.SourceURL
		}
		if rec.SeanceRef != "" {
			metadata["seance_ref"] = rec.SeanceRef
		}

		// Build original quote that includes law key and titles for traceability
		originalQuote := fmt.Sprintf("Law Key: %s\nFull Title: %s\nShort Title: %s\nSummary: %s",
			rec.LawKey, rec.LawTitle, rec.LawShortTitle, rec.LawSummary)
		if rec.SourceURL != "" {
			originalQuote += fmt.Sprintf("\nSource: %s", rec.SourceURL)
		}

		inputs = append(inputs, core.StoreFactInput{
			SubjectEntity:      rec.PersonName,
			SubjectEntityType:  rec.EntityType,
			NormalizedFactText: rec.FactText,
			EmbeddingText:      rec.EmbeddingText,
			Embedding:          emb,
			OriginalQuote:      originalQuote,
			CanonicalVerb:      rec.CanonicalVerb,
			SourceURL:          rec.SourceURL,
			PublishedAt:        rec.PublishedAt,
			SourceType:         "assemblee_law_page",
			Metadata:           metadata,
			Lang:               "fr",
		})
	}
	return inputs, nil
}
