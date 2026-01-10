package frpolitics

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"
)

// FactRecord captures a single fact to be stored using storage layer
type FactRecord struct {
	// PersonName is the normalized person name to mention (human or party)
	PersonName string
	// FactText is the normalized fact text
	FactText string
	// Topics are derived topics
	Topics []string
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

	// Collect unique fact texts for embedding generation
	factTexts := make([]string, 0, len(factTextToRecords))
	for factText := range factTextToRecords {
		factTexts = append(factTexts, factText)
	}

	// Generate embeddings in batches
	batchSize := 50
	embeddingsMap := make(map[string][]float32, len(factTexts))

	for i := 0; i < len(factTexts); i += batchSize {
		end := i + batchSize
		if end > len(factTexts) {
			end = len(factTexts)
		}

		batchTexts := factTexts[i:end]
		embs, err := embGen.GenerateEmbeddings(ctx, batchTexts)
		if err != nil {
			return fmt.Errorf("failed to generate embeddings for batch %d-%d: %w", i, end, err)
		}

		for j, text := range batchTexts {
			embeddingsMap[text] = embs[j]
		}
	}

	// Store each unique fact with all associated persons
	for factText, records := range factTextToRecords {
		if len(records) == 0 {
			continue
		}

		// Use the first record for metadata (they should all have the same law info)
		firstRec := records[0]

		// Collect all person names associated with this fact
		personNames := make([]string, 0, len(records))
		for _, rec := range records {
			personNames = append(personNames, rec.PersonName)
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

		input := storage.StoreFactInput{
			NormalizedFactText: factText,
			Embedding:          embeddingsMap[factText],
			OriginalQuote:      originalQuote,
			Topics:             firstRec.Topics,
			InvolvedPeople:     personNames,
			SourceURL:          firstRec.SourceURL,
			PublishedAt:        firstRec.PublishedAt,
			SourceType:         "assemblee_law_page",
			Metadata:           metadata,
		}
		_, _, _, err := storer.StoreFact(ctx, input)
		if err != nil {
			return fmt.Errorf("failed to store fact '%s': %w", factText[:50], err)
		}
	}

	return nil
}
