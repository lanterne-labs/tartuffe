package cmd

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"golang.org/x/time/rate"
)

var (
	searchResultLimit int // Variable to hold the flag value
)

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the fact extraction pipeline once.",
	Long: `Runs the full pipeline: searches for content about target persons,
 extracts facts, generates embeddings, and stores results in the database,
 handling duplicates.`,
	Run: runPipeline,
}

func init() {
	cronCmd.AddCommand(runCmd)

	// Flags for the run command, e.g., specifying target persons
	// runCmd.Flags().StringSliceP("persons", "p", []string{}, "List of persons to process (comma-separated)")

	// Add flag for search result limit
	runCmd.Flags().IntVarP(&searchResultLimit, "search-limit", "l", 1, "Maximum number of search results per person")
}

// Helper struct to hold fact and its context during processing
type factProcessingUnit struct {
	fact          backend.Fact
	publishedDate *time.Time
	sourceURL     string
	embedding     []float32 // Added field to store pre-generated embedding
}

const maxEmbeddingBatchSize = 50   // Max items per Mistral embedding API call
const factExtractionRateLimit = 1  // Allowed requests per second to the fact extractor LLM
const factExtractionBurstLimit = 1 // Burst allowed for fact extractor

// runPipeline is the main function executing the fact extraction workflow.
func runPipeline(cmd *cobra.Command, args []string) {
	ctx := context.Background()

	// --- 1. Configuration & Initialization ---
	log.Info().Msg("Starting fact extraction pipeline...")

	// Load environment variables (.env file)
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg("INFO: .env file not found or error loading, relying on environment variables.")
	}

	// Initialize dependencies
	embedder, err := embedding.NewMistralEmbeddingGeneratorFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create embedder")
	}

	factExtractor, err := categorizer.NewMistralExtractorFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create fact extractor")
	}

	// Pass the initialized embedder to the storer constructor
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer")
	}
	defer storer.Close()
	log.Info().Msg("Database connection successful.")

	// Initialize Search Provider (Google Custom Search)
	// Requires GOOGLE_API_KEY and GOOGLE_CSE_ID environment variables
	searchProvider, err := search.NewGoogleSearchProviderFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize Google Search Provider. Ensure GOOGLE_API_KEY and GOOGLE_CSE_ID are set.")
	}
	log.Info().Msg("Search provider initialized (Google Custom Search).")

	// Initialize Content Extractor (Apify)
	contentExtractor, err := extractor.NewApifyExtractorFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize Apify content extractor")
	}
	log.Info().Msg("Content extractor initialized (Apify).")

	// Initialize Rate Limiter for Fact Extraction
	factLimiter := rate.NewLimiter(rate.Limit(factExtractionRateLimit), factExtractionBurstLimit)
	log.Info().Float64("requests_per_sec", float64(factExtractionRateLimit)).Int("burst", factExtractionBurstLimit).Msg("Fact extraction rate limiter initialized")

	// Initialize Embedder (Mistral)
	embedder, err = embedding.NewMistralEmbeddingGeneratorFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to initialize Mistral embedder")
	}
	log.Info().Msg("Embedding generator initialized (Mistral).")

	// --- 2. Get Target Persons from Database ---
	targetPersons, err := storer.GetAllPersons(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to fetch target persons from database")
	}
	if len(targetPersons) == 0 {
		log.Info().Msg("No persons found in the database to process. Exiting.")
		return
	}
	log.Info().Int("count", len(targetPersons)).Strs("persons", targetPersons).Msg("Found persons in database to process")

	// --- 3. Collect All Facts to Process ---
	var allUnitsToProcess []factProcessingUnit // Collect facts + context

	log.Info().Msg("Starting URL search and content/fact extraction phase...")
	for _, personName := range targetPersons {
		log.Info().Str("person", personName).Msg("Processing Person")

		// a. Search for URLs
		log.Debug().Int("limit", searchResultLimit).Msg("Performing search with limit")
		searchOpts := map[string]string{"num": strconv.Itoa(searchResultLimit)}
		searchResults, searchErr := searchProvider.SearchWithOptions(personName, searchOpts)
		if searchErr != nil {
			log.Error().Err(searchErr).Str("person", personName).Msg("Failed to search for URLs. Skipping person.")
			continue
		}
		if len(searchResults) == 0 {
			log.Info().Str("person", personName).Msg("No new URLs found via search.")
			continue
		}
		log.Info().Str("person", personName).Int("limit_requested", searchResultLimit).Int("results_found", len(searchResults)).Msg("Found potential URLs")

		// b. Process each URL (Extraction Only)
		for _, result := range searchResults {
			url := result.URL
			if url == "" {
				log.Warn().Str("person", personName).Msg("Search result has empty URL. Skipping result.")
				continue
			}
			log.Debug().Str("url", url).Str("title", result.Title).Msg("Processing URL for extraction")

			// i. Check URL existence
			exists, err := storer.CheckSourceURLExists(ctx, url)
			if err != nil {
				log.Error().Err(err).Str("url", url).Msg("Failed to check if URL exists. Skipping URL.")
				continue
			}
			if exists {
				log.Debug().Str("url", url).Msg("URL already processed, skipping extraction.")
				continue
			}

			// ii. Extract Content
			content, err := contentExtractor.ExtractContent(url)
			if err != nil {
				log.Error().Err(err).Str("url", url).Msg("Failed to extract content. Skipping URL.")
				continue
			}
			if content == nil || strings.TrimSpace(content.Content) == "" {
				log.Warn().Str("url", url).Msg("No content extracted, skipping URL.")
				continue
			}
			log.Debug().Str("url", url).Msg("Content extracted successfully")

			// iii. Extract Facts (with Rate Limiting)
			log.Debug().Str("url", url).Msg("Waiting for rate limiter before fact extraction...")
			err = factLimiter.Wait(ctx) // Wait for limiter
			if err != nil {
				// Handle context cancellation if necessary (e.g., Ctrl+C)
				log.Error().Err(err).Str("url", url).Msg("Fact extraction rate limiter wait interrupted. Skipping URL.")
				continue
			}
			log.Debug().Str("url", url).Msg("Rate limiter allowed access, calling fact extractor.")

			facts, err := factExtractor.ExtractFacts(ctx, personName, *content)
			if err != nil {
				log.Error().Err(err).Str("person", personName).Str("url", url).Msg("Failed to extract facts. Skipping URL.")
				continue
			}
			if len(facts) == 0 {
				log.Info().Str("person", personName).Str("url", url).Msg("No facts extracted.")
				continue
			}
			log.Info().Str("person", personName).Str("url", url).Int("fact_count", len(facts)).Msg("Extracted facts, adding to collection")

			// iv. Add valid facts to the collection
			for i, fact := range facts {
				if fact.NormalizedFactText == "" || len(fact.InvolvedPeople) == 0 {
					log.Warn().Int("fact_index", i).Str("url", url).Msg("Skipping extracted fact due to missing normalized text or involved people.")
					continue
				}
				allUnitsToProcess = append(allUnitsToProcess, factProcessingUnit{
					fact:          fact,
					publishedDate: content.DatePublished,
					sourceURL:     url,
				})
			}
		}
	}

	log.Info().Int("total_facts_collected", len(allUnitsToProcess)).Msg("Finished extraction phase.")

	// --- 4. Batch Embedding Generation ---
	if len(allUnitsToProcess) == 0 {
		log.Info().Msg("No valid facts collected to process. Exiting.")
		return
	}

	log.Info().Int("total_facts", len(allUnitsToProcess)).Msg("Starting batch embedding generation...")
	embeddingErrors := 0

	for i := 0; i < len(allUnitsToProcess); i += maxEmbeddingBatchSize {
		end := i + maxEmbeddingBatchSize
		if end > len(allUnitsToProcess) {
			end = len(allUnitsToProcess)
		}
		currentBatchUnits := allUnitsToProcess[i:end]
		textsInBatch := make([]string, len(currentBatchUnits))
		for j, unit := range currentBatchUnits {
			textsInBatch[j] = unit.fact.NormalizedFactText
		}

		batchNum := (i / maxEmbeddingBatchSize) + 1
		log.Debug().Int("batch_num", batchNum).Int("batch_size", len(textsInBatch)).Msg("Processing embedding batch")

		batchResultEmbeddings, err := embedder.GenerateEmbeddings(ctx, textsInBatch)
		if err != nil {
			log.Error().Err(err).Int("batch_num", batchNum).Int("batch_size", len(textsInBatch)).Msg("Failed to generate embeddings for batch. Will skip facts in this batch.")
			embeddingErrors++
			// Mark embeddings as nil for this batch
			for j := 0; j < len(currentBatchUnits); j++ {
				allUnitsToProcess[i+j].embedding = nil
			}
			continue // Skip to next batch
		}

		if len(batchResultEmbeddings) != len(textsInBatch) {
			log.Error().Int("batch_num", batchNum).Int("expected", len(textsInBatch)).Int("received", len(batchResultEmbeddings)).Msg("Embedder returned mismatched number of embeddings for batch. Will skip facts in this batch.")
			embeddingErrors++
			// Mark embeddings as nil for this batch
			for j := 0; j < len(currentBatchUnits); j++ {
				allUnitsToProcess[i+j].embedding = nil
			}
			continue
		}

		// Assign embeddings back to the corresponding units
		for j, embedding := range batchResultEmbeddings {
			if embedding == nil || len(embedding) == 0 {
				log.Warn().Int("batch_num", batchNum).Int("item_index_in_batch", j).Str("norm_text", textsInBatch[j]).Msg("Embedder returned nil/empty embedding for an item. Will skip this fact.")
				allUnitsToProcess[i+j].embedding = nil // Ensure it's marked as nil
				embeddingErrors++
			} else {
				allUnitsToProcess[i+j].embedding = embedding // Assign the valid embedding
			}
		}
	}
	log.Info().Int("embedding_errors", embeddingErrors).Int("total_facts", len(allUnitsToProcess)).Msg("Finished batch embedding generation.")

	// --- 5. Process Facts (Similarity Check & Storage) ---
	log.Info().Msg("Starting fact processing (similarity check & storage)...")
	processedCount := 0
	skippedAsDuplicateCount := 0
	storageErrors := 0

	// Process one fact at a time, using pre-generated embeddings
	for _, unit := range allUnitsToProcess {
		// Skip if embedding generation failed for this unit
		if unit.embedding == nil {
			log.Warn().Str("url", unit.sourceURL).Str("norm_text", unit.fact.NormalizedFactText).Msg("Skipping fact processing because embedding generation failed earlier.")
			storageErrors++ // Count as error if embedding failed
			continue
		}

		log.Debug().Str("url", unit.sourceURL).Str("norm_text", unit.fact.NormalizedFactText).Msg("Processing collected fact unit")

		// a. Check for Similarity using FindSimilarFacts
		// Use a transaction for the check
		txCheck, err := storer.Pool().Begin(ctx)
		if err != nil {
			log.Error().Err(err).Str("norm_text", unit.fact.NormalizedFactText).Str("url", unit.sourceURL).Msg("Failed to begin transaction for similarity check. Skipping fact.")
			storageErrors++
			continue
		}

		// Call FindSimilarFacts which returns a slice of matches within the threshold
		similarFacts, err := storer.FindSimilarFacts(ctx, txCheck, unit.embedding) // Correct call
		_ = txCheck.Rollback(ctx)                                                  // Always rollback check transaction

		if err != nil {
			log.Error().Err(err).Str("norm_text", unit.fact.NormalizedFactText).Str("url", unit.sourceURL).Msg("Error during similarity check. Skipping fact.")
			storageErrors++
			continue
		}

		// b. Handle based on similarity check result
		if len(similarFacts) > 0 {
			// Found similar facts - Link this source to the *closest* existing fact (Simplified call)
			closestMatch := similarFacts[0] // First result is the closest
			log.Info().Int64("existing_fact_id", closestMatch.ID).Float64("distance", closestMatch.Distance).Str("new_source_url", unit.sourceURL).Msg("Found similar fact based on embedding. Linking source.")

			linkInput := storage.LinkExistingFactToSourceInput{
				ExistingFactID: closestMatch.ID, // Link to the closest one only
				SourceURL:      unit.sourceURL,
				OriginalQuote:  unit.fact.OriginalQuote,
				PublishedAt:    unit.publishedDate,
			}
			_, err := storer.LinkExistingFactToSource(ctx, linkInput)
			if err != nil {
				log.Error().Err(err).Int64("existing_fact_id", closestMatch.ID).Str("url", unit.sourceURL).Msg("Failed to link source to existing fact.")
				storageErrors++
			} else {
				skippedAsDuplicateCount++
			}
		} else {
			// No similar facts found - Store the new fact
			log.Debug().Str("norm_text", unit.fact.NormalizedFactText).Str("url", unit.sourceURL).Msg("No similar facts found. Storing as new fact.")
			storeInput := storage.StoreFactInput{
				NormalizedFactText: unit.fact.NormalizedFactText,
				Embedding:          unit.embedding, // Use the pre-generated embedding
				OriginalQuote:      unit.fact.OriginalQuote,
				Topics:             unit.fact.Topics,
				InvolvedPeople:     unit.fact.InvolvedPeople,
				SourceURL:          unit.sourceURL,
				PublishedAt:        unit.publishedDate,
			}
			factID, factSourceID, _, err := storer.StoreFact(ctx, storeInput)
			if err != nil {
				log.Error().Err(err).Str("fact", unit.fact.NormalizedFactText).Str("url", unit.sourceURL).Msg("Failed to store fact")
				continue // Skip this fact
			}
			log.Info().Int64("factID", factID).Int64("factSourceID", factSourceID).Str("fact", unit.fact.NormalizedFactText).Msg("Stored fact")

			// CONTRAdiction check logic placeholder...

			processedCount++
		}
	}

	// Log summary - Renamed stage 4 to 5
	log.Info().Int("processed", processedCount).Int("linked_as_duplicate", skippedAsDuplicateCount).Int("errors", storageErrors).Msg("Finished fact processing and storage phase (Stage 5).")
	log.Info().Msg("Fact extraction pipeline finished.")
}
