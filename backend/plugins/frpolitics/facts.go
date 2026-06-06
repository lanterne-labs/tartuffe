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
	// FactText is the normalized fact text (without person names for display)
	FactText string
	// EmbeddingText is the text used for generating embeddings (without person names)
	// For frpolitics, this equals FactText since FactText is already name-free
	EmbeddingText string
	// CanonicalVerb is the canonical verb (VOTED_FOR, VOTED_AGAINST, etc.)
	CanonicalVerb string
	// SourceURL is the canonical law page URL
	SourceURL string
	// LawKey is a minimal identifier recorded in fact source metadata
	LawKey string
	// LawTitle is the full legal title of the law
	LawTitle string
	// LawShortTitle is the AI-derived short title
	LawShortTitle string
	// LawSummary is the AI-derived summary
	LawSummary string
	// PublishedAt is the vote datetime if known
	PublishedAt *time.Time
}

// StoreFactsGrouped generates embeddings once per unique fact text and links all associated persons
func StoreFactsGrouped(ctx context.Context, storer *storage.PostgresStorer, factTextToRecords map[string][]FactRecord) error {
	if len(factTextToRecords) == 0 {
		return nil
	}

	// Try to initialize embedding generator
	embGen, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize embedding generator: %w", err)
	}

	// Collect unique embedding texts for embedding generation
	// For frpolitics, EmbeddingText equals FactText since both are name-free
	embeddingTexts := make([]string, 0, len(factTextToRecords))
	embeddingTextToFactText := make(map[string]string, len(factTextToRecords))
	for factText, records := range factTextToRecords {
		if len(records) > 0 {
			embText := records[0].EmbeddingText
			if embText == "" {
				embText = factText // Fallback if EmbeddingText not set
			}
			embeddingTexts = append(embeddingTexts, embText)
			embeddingTextToFactText[embText] = factText
		}
	}

	// Generate embeddings in batches using EmbeddingText (name-free version)
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

	// Build inputs for the core fact inserter
	totalFacts := len(factTextToRecords)
	inputs := make([]core.StoreFactInput, 0, totalFacts)

	log.Info().
		Int("total_facts", totalFacts).
		Msg("frpolitics: preparing facts for insertion")

	for factText, records := range factTextToRecords {
		if len(records) == 0 {
			continue
		}

		// Use the first record for metadata (they should all have the same law info)
		firstRec := records[0]

		// Get embedding text (name-free version)
		embeddingText := firstRec.EmbeddingText
		if embeddingText == "" {
			embeddingText = factText // Fallback if EmbeddingText not set
		}

		// First person is the subject entity, rest are secondary mentions
		subjectEntity := firstRec.PersonName
		var secondaryPersons []string
		if len(records) > 1 {
			secondaryPersons = make([]string, 0, len(records)-1)
			for _, rec := range records[1:] {
				secondaryPersons = append(secondaryPersons, rec.PersonName)
			}
		}

		// Ensure metadata includes law information for traceability
		metadata := map[string]any{
			"law_key":         firstRec.LawKey,
			"law_title":       firstRec.LawTitle,
			"law_short_title": firstRec.LawShortTitle,
			"law_summary":     firstRec.LawSummary,
		}
		if firstRec.SourceURL != "" {
			metadata["source_url"] = firstRec.SourceURL
		}

		// Build original quote that includes law key and titles for traceability
		originalQuote := fmt.Sprintf("Law Key: %s\nFull Title: %s\nShort Title: %s\nSummary: %s",
			firstRec.LawKey, firstRec.LawTitle, firstRec.LawShortTitle, firstRec.LawSummary)
		if firstRec.SourceURL != "" {
			originalQuote += fmt.Sprintf("\nSource: %s", firstRec.SourceURL)
		}

		input := core.StoreFactInput{
			SubjectEntity:      subjectEntity,
			NormalizedFactText: factText,
			EmbeddingText:      embeddingText, // Name-free version for embeddings
			Embedding:          embeddingsMap[embeddingText],
			OriginalQuote:      originalQuote,
			CanonicalVerb:      firstRec.CanonicalVerb,
			InvolvedPeople:     secondaryPersons,
			SourceURL:          firstRec.SourceURL,
			PublishedAt:        firstRec.PublishedAt,
			SourceType:         "assemblee_law_page",
			Metadata:           metadata,
			Lang:               "fr",
		}
		inputs = append(inputs, input)
	}

	stanceMgr, err := core.NewStanceManagerFromEnv(storage.NewStanceStorerAdapter(storer))
	if err != nil {
		return fmt.Errorf("stance manager required for fact ingestion: %w", err)
	}
	inserter := core.NewFactInserter(storer, stanceMgr)
	inserter.EnableLLMDedupe()

	log.Info().
		Int("total_facts", len(inputs)).
		Msg("frpolitics: starting fact insertion with similarity detection")

	if _, err := inserter.InsertFacts(ctx, inputs); err != nil {
		return fmt.Errorf("failed to insert facts: %w", err)
	}

	log.Info().
		Int("total_facts", len(inputs)).
		Msg("frpolitics: fact insertion complete")

	return nil
}
