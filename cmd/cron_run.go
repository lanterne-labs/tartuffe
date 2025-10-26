package cmd

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util" // Added for MinInt
	"errors"                      // Import errors package
	"fmt"                         // Import fmt for error wrapping
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
	pgvector "github.com/pgvector/pgvector-go" // Correct import for pgvector.Vector
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"golang.org/x/time/rate"
)

var (
	searchResultLimit     int    // Variable to hold the flag value
	personNameFlag        string // Variable to hold the single person flag value
	debugScrapeFolderFlag string // Variable to hold the debug scrape folder path
	revisitIntervalHours  int    // Variable to hold the revisit interval in hours
)

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Run the fact extraction pipeline once for specified or all persons.",
	Long: `Runs the full pipeline: searches for content, extracts facts, generates embeddings, 
stores results, checks for contradictions, and handles duplicates. 
By default, processes all persons found in the database. Use the --person flag 
to process only a specific person. The --search-limit flag applies to the number of 
URLs to process that are either new or due for a revisit. The --revisit-interval-hours 
flag controls how often an already processed URL can be revisited.`,
	RunE: executeCronRun,
}

func init() {
	cronCmd.AddCommand(runCmd)
	// Add flag for search result limit
	runCmd.Flags().IntVarP(&searchResultLimit, "search-limit", "l", 5, "Maximum number of URLs to process (new or revisit) per person")
	// Add flag for specific person
	runCmd.Flags().StringVarP(&personNameFlag, "person", "p", "", "Specify a single person name to process (optional)")
	runCmd.Flags().StringVar(&debugScrapeFolderFlag, "debug-scrape-folder", "", "Path to a folder where raw scraped content will be saved (optional)")
	// Add flag for revisit interval
	runCmd.Flags().IntVar(&revisitIntervalHours, "revisit-interval-hours", 24, "Minimum hours before revisiting a previously processed URL")
}

// Helper struct to hold fact and its context during processing
type factProcessingUnit struct {
	fact           backend.Fact
	publishedDate  *time.Time
	sourceURL      string
	embedding      pgvector.Vector // Use the correct pgvector.Vector type
	validEmbedding bool            // Flag to indicate if embedding was successfully generated
}

type appDependencies struct {
	embedder              *embedding.OpenAIEmbeddingGenerator
	factExtractor         *categorizer.OpenAIExtractor
	contradictionDetector *contradiction.OpenAIContradictionDetector
	storer                *storage.PostgresStorer
	searchProvider        *search.GoogleSearchProvider
	contentExtractor      extractor.ContentExtractor
	factLimiter           *rate.Limiter
}

const maxEmbeddingBatchSize = 50   // Max items per embedding API call
const factExtractionRateLimit = 1  // Allowed requests per second to the fact extractor LLM
const factExtractionBurstLimit = 1 // Burst allowed for fact extractor
const googleSearchBatchSize = 10   // Google API 'num' parameter max is 10

var nonAlphanumericRegex = regexp.MustCompile(`[^a-zA-Z0-9]+`)

func sanitizeFilename(name string) string {
	// Replace non-alphanumeric characters with a hyphen
	name = nonAlphanumericRegex.ReplaceAllString(name, "-")
	// Trim leading/trailing hyphens that might result
	name = strings.Trim(name, "-")
	// Limit length to avoid overly long filenames
	maxLength := 100
	if len(name) > maxLength {
		name = name[:maxLength]
	}
	return name
}

// initializeDependencies sets up all necessary external services and configurations.
func initializeDependencies(ctx context.Context) (*appDependencies, error) {
	// Load environment variables (.env file)
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg("INFO: .env file not found or error loading, relying on environment variables.")
	}

	deps := &appDependencies{}
	var err error

	// Initialize services that don't depend on API key order first
	deps.embedder, err = embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to create embedder: %w", err)
	}
	deps.factExtractor, err = categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to create fact extractor: %w", err)
	}
	deps.contradictionDetector, err = contradiction.NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		return nil, fmt.Errorf("failed to create contradiction detector: %w", err)
	}
	deps.storer, err = storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	// Defer closing the storer only after we know initialization succeeded fully
	// We'll handle closing manually on errors below
	log.Info().Msg("Database connection successful.")

	deps.searchProvider, err = search.NewGoogleSearchProviderFromEnv()
	if err != nil {
		deps.storer.Close() // Close DB if search provider fails
		return nil, fmt.Errorf("failed to initialize Google Search Provider: %w", err)
	}
	log.Info().Msg("Search provider initialized (Google Custom Search).")

	// --- Initialize Content Extractor (Try Firecrawl, then Apify) ---
	log.Info().Msg("Attempting to initialize content extractor...")

	// Define initializer function type returning the interface
	type extractorInitializer func() (extractor.ContentExtractor, error)

	// Wrap concrete initializers to return the interface type
	initializers := []extractorInitializer{
		func() (extractor.ContentExtractor, error) { // Adapter for Firecrawl
			return extractor.NewFirecrawlExtractorFromEnv()
		},
		func() (extractor.ContentExtractor, error) { // Adapter for Apify
			return extractor.NewApifyExtractorFromEnv()
		},
	}

	var initializedExtractor extractor.ContentExtractor
	var initErr error

	for _, initFunc := range initializers {
		extractorInstance, err := initFunc()
		if err != nil {
			log.Error().Err(err).Msg("Unexpected error during extractor initialization attempt")
			initErr = err
			break
		}
		if extractorInstance != nil {
			initializedExtractor = extractorInstance
			log.Info().Msgf("Initialized content extractor: %T", initializedExtractor)
			break
		}
	}

	// Check if any extractor was initialized
	if initializedExtractor == nil {
		deps.storer.Close() // Close DB connection as we cannot proceed
		if initErr != nil {
			// Return the critical error encountered during initialization attempts
			return nil, fmt.Errorf("failed to initialize any content extractor due to error: %w", initErr)
		} else {
			// No critical errors, but no API keys found
			return nil, fmt.Errorf("failed to initialize content extractor: no API key found for supported services (e.g., FIRECRAWL_API_KEY, APIFY_API_KEY)")
		}
	}
	deps.contentExtractor = initializedExtractor

	// Initialize other dependencies (like rate limiter)
	deps.factLimiter = rate.NewLimiter(rate.Limit(factExtractionRateLimit), factExtractionBurstLimit)
	log.Info().Float64("requests_per_sec", float64(factExtractionRateLimit)).Int("burst", factExtractionBurstLimit).Msg("Fact extraction rate limiter initialized")

	return deps, nil // All dependencies initialized successfully
}

// collectFactUnits searches for URLs, extracts content and facts for target persons.
// It now attempts to process 'searchResultLimit' NEW URLs per person by fetching search results in batches.
func collectFactUnits(ctx context.Context, deps *appDependencies) ([]factProcessingUnit, error) {
	var allUnitsToProcess []factProcessingUnit
	var targetPersons []string
	var err error

	// Check if a specific person was provided via flag
	if personNameFlag != "" {
		log.Info().Str("person", personNameFlag).Msg("Processing specific person provided via flag")
		targetPersons = []string{personNameFlag}
		// Note: findOrInsertPersons within StoreFact will handle adding this person if new.
	} else {
		log.Info().Msg("No specific person provided, fetching all persons from database")
		targetPersons, err = deps.storer.GetAllPersons(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to fetch target persons from database: %w", err)
		}
		if len(targetPersons) == 0 {
			log.Info().Msg("No persons found in the database to process.")
			return allUnitsToProcess, nil // Return empty slice, not an error
		}
		log.Info().Int("count", len(targetPersons)).Strs("persons", targetPersons).Msg("Found persons in database to process")
	}

	log.Info().Msg("Starting URL search and content/fact extraction phase...")
	for _, personName := range targetPersons {
		log.Info().Str("person", personName).Msg("Processing Person")
		processedURLCountForThisPersonThisRun := 0
		googleSearchStartIndex := 1 // Google's 'start' parameter is 1-indexed

		// Loop to fetch search results in batches until enough new URLs are processed or no more results
		for processedURLCountForThisPersonThisRun < searchResultLimit {
			numToFetch := googleSearchBatchSize
			// If remaining needed is less than batch size, fetch only remaining needed.
			// However, fetch a bit more (up to batch size) if only 1 or 2 are left, to improve chances of finding a new one quickly.
			remainingNeeded := searchResultLimit - processedURLCountForThisPersonThisRun
			if remainingNeeded < googleSearchBatchSize {
				numToFetch = remainingNeeded
				if numToFetch <= 2 { // If 1 or 2 left, try to get a few more to avoid many small API calls
					numToFetch = util.MinInt(googleSearchBatchSize, numToFetch+3)
				}
			}
			if numToFetch <= 0 { // Should not happen if processedURLCountForThisPersonThisRun < searchResultLimit, but as safeguard
				break
			}

			log.Debug().Str("person", personName).Int("num_to_fetch", numToFetch).Int("start_index", googleSearchStartIndex).Msg("Performing search batch")
			// Modify the search query to be more specific
			searchQuery := personName + " news"
			searchOpts := map[string]string{
				"num":   strconv.Itoa(numToFetch),
				"start": strconv.Itoa(googleSearchStartIndex),
			}
			searchResults, searchErr := deps.searchProvider.SearchWithOptions(searchQuery, searchOpts)
			if searchErr != nil {
				log.Error().Err(searchErr).Str("person", personName).Msg("Failed to search for URLs for this batch. Stopping search for this person.")
				break // Break from batch fetching loop for this person
			}

			if len(searchResults) == 0 {
				log.Info().Str("person", personName).Int("start_index", googleSearchStartIndex).Msg("No more URLs found from search provider for this person in this batch or subsequent.")
				break // No more results, break from batch fetching loop
			}

			log.Info().Str("person", personName).Int("batch_results_found", len(searchResults)).Int("start_index", googleSearchStartIndex).Msg("Found potential URLs in batch")

			// Loop over search results from the current batch
			for _, searchRes := range searchResults {
				urlToProcess := searchRes.URL
				log.Debug().Str("person", personName).Str("url", urlToProcess).Msg("Considering URL")

				// 1. Check if URL is visitable (new or revisit interval passed)
				visitable, sourceExists, err := deps.storer.IsURLVisitable(ctx, urlToProcess, revisitIntervalHours)
				if err != nil {
					log.Error().Err(err).Str("url", urlToProcess).Msg("Failed to check if URL is visitable. Skipping URL.")
					continue
				}

				if !visitable {
					if sourceExists {
						log.Info().Str("url", urlToProcess).Int("revisit_interval_hours", revisitIntervalHours).Msg("URL already processed and not yet due for revisit. Skipping.")
					} else {
						// This case should ideally not be hit if IsURLVisitable logic is correct (new URLs are always visitable)
						log.Warn().Str("url", urlToProcess).Msg("URL marked not visitable but does not exist in sources. Skipping.")
					}
					continue
				}

				// If we are here, the URL is visitable (either new or past its revisit time)
				log.Info().Str("url", urlToProcess).Bool("is_new", !sourceExists).Msg("URL is visitable, proceeding with content extraction.")

				// 2. Extract Content
				content, err := deps.contentExtractor.ExtractContent(urlToProcess)
				if err != nil {
					log.Error().Err(err).Str("url", urlToProcess).Msg("Failed to extract content. Skipping URL.")
					continue
				}
				if content == nil || strings.TrimSpace(content.Content) == "" {
					log.Warn().Str("url", urlToProcess).Msg("No content extracted, skipping URL.")
					continue
				}
				log.Debug().Str("url", urlToProcess).Msg("Content extracted successfully")

				if debugScrapeFolderFlag != "" {
					if err := os.MkdirAll(debugScrapeFolderFlag, 0755); err != nil {
						log.Error().Err(err).Str("folder", debugScrapeFolderFlag).Msg("Failed to create debug scrape folder. Content will not be saved.")
					} else {
						filename := filepath.Join(debugScrapeFolderFlag, fmt.Sprintf("%s_%s.md", sanitizeFilename(personName), sanitizeFilename(urlToProcess)))
						if err := os.WriteFile(filename, []byte(content.Content), 0644); err != nil {
							log.Error().Err(err).Str("filename", filename).Msg("Failed to write scraped content to debug file.")
						} else {
							log.Info().Str("filename", filename).Msg("Successfully saved raw scraped content to debug file.")
						}
					}
				}

				// Log the extracted content before sending to fact extractor
				log.Debug().Str("url", urlToProcess).Str("extracted_content_snippet", summarizedContent(content.Content, 500)).Msg("Content snippet for fact extraction")

				log.Debug().Str("url", urlToProcess).Msg("Waiting for rate limiter before fact extraction...")
				err = deps.factLimiter.Wait(ctx)
				if err != nil {
					log.Error().Err(err).Str("url", urlToProcess).Msg("Fact extraction rate limiter wait interrupted. Skipping URL.")
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return nil, fmt.Errorf("context cancelled during fact extraction rate limit wait: %w", err)
					}
					continue
				}
				log.Debug().Str("url", urlToProcess).Msg("Rate limiter allowed access, calling fact extractor.")

				facts, err := deps.factExtractor.ExtractFacts(ctx, personName, *content)
				if err != nil {
					log.Error().Err(err).Str("url", urlToProcess).Str("person_name", personName).Msg("Failed to extract facts for content. Skipping content.")
					continue
				}
				if len(facts) == 0 {
					log.Info().Str("url", urlToProcess).Str("person_name", personName).Msg("No facts extracted from content.")
					continue
				}
				log.Info().Str("url", urlToProcess).Int("fact_count", len(facts)).Msg("Successfully extracted facts from content")

				for _, fact := range facts {
					unit := factProcessingUnit{
						fact:          fact,
						publishedDate: content.DatePublished,
						sourceURL:     urlToProcess,
					}
					allUnitsToProcess = append(allUnitsToProcess, unit)
				}

				// Successfully processed this URL (content and facts extracted)
				processedURLCountForThisPersonThisRun++ // Increment counter for processed URLs for this person in this run
				if processedURLCountForThisPersonThisRun >= searchResultLimit {
					log.Info().Str("person", personName).Int("limit", searchResultLimit).Msg("Reached search-limit for this person. Moving to next person or finishing.")
					break // Break from processing URLs for this person
				}
			} // End loop over search results in batch

			// If we've reached the search limit for the person, break from the batch fetching loop too
			if processedURLCountForThisPersonThisRun >= searchResultLimit {
				break
			}

			googleSearchStartIndex += len(searchResults) // Prepare for the next batch
			if len(searchResults) < numToFetch {         // If fewer results returned than asked for, likely no more results
				log.Info().Str("person", personName).Msg("Search provider returned fewer results than requested in batch, assuming no more URLs for this person.")
				break // Break from batch fetching loop
			}
		} // End loop for fetching batches of search results

		log.Info().Str("person", personName).Int("urls_processed_this_run", processedURLCountForThisPersonThisRun).Msg("Finished processing person")

	} // End loop over targetPersons

	log.Info().Int("total_units_for_embedding", len(allUnitsToProcess)).Msg("Finished collecting all fact units for all persons.")
	return allUnitsToProcess, nil
}

// generateEmbeddingsInPlace generates embeddings for the collected facts in batches.
func generateEmbeddingsInPlace(ctx context.Context, deps *appDependencies, unitsToProcess []factProcessingUnit) error {
	if len(unitsToProcess) == 0 {
		log.Debug().Msg("No fact units to generate embeddings for.")
		return nil
	}
	log.Info().Int("unit_count", len(unitsToProcess)).Msg("Starting embedding generation for fact units...")
	textsToEmbed := make([]string, 0, len(unitsToProcess))
	for _, unit := range unitsToProcess {
		textsToEmbed = append(textsToEmbed, unit.fact.NormalizedFactText)
	}
	for i := 0; i < len(textsToEmbed); i += maxEmbeddingBatchSize {
		end := i + maxEmbeddingBatchSize
		if end > len(textsToEmbed) {
			end = len(textsToEmbed)
		}
		batchTexts := textsToEmbed[i:end]
		log.Debug().Int("batch_size", len(batchTexts)).Int("start_index", i).Msg("Generating embeddings for batch")
		embeddings, err := deps.embedder.GenerateEmbeddings(ctx, batchTexts)
		if err != nil {
			log.Error().Err(err).Int("batch_start_index", i).Msg("Failed to generate embeddings for a batch. Marking affected units as invalid.")
			for j := 0; j < len(batchTexts); j++ {
				unitsToProcess[i+j].validEmbedding = false
			}
			continue // Try next batch
		}
		if len(embeddings) != len(batchTexts) {
			log.Error().Int("expected_count", len(batchTexts)).Int("received_count", len(embeddings)).Msg("Mismatch in expected and received embeddings count. Marking batch as invalid.")
			for j := 0; j < len(batchTexts); j++ {
				unitsToProcess[i+j].validEmbedding = false
			}
			continue
		}
		for j, emb := range embeddings {
			if len(emb) == 0 {
				log.Warn().Str("text", batchTexts[j]).Msg("Embedder returned empty embedding for text. Marking unit as invalid.")
				unitsToProcess[i+j].validEmbedding = false
			} else {
				unitsToProcess[i+j].embedding = pgvector.NewVector(emb)
				unitsToProcess[i+j].validEmbedding = true
			}
		}
	}
	validCount := 0
	for _, unit := range unitsToProcess {
		if unit.validEmbedding {
			validCount++
		}
	}
	log.Info().Int("valid_embeddings", validCount).Int("total_units", len(unitsToProcess)).Msg("Embedding generation completed.")
	return nil
}

// storeAndCheckContradictions stores facts and checks for contradictions.
func storeAndCheckContradictions(ctx context.Context, deps *appDependencies, unitsToProcess []factProcessingUnit) error {
	if len(unitsToProcess) == 0 {
		log.Debug().Msg("No fact units to store or check for contradictions.")
		return nil
	}
	log.Info().Int("unit_count", len(unitsToProcess)).Msg("Starting storage and contradiction checking...")
	totalFactsStored := 0

	for i, unit := range unitsToProcess {
		if !unit.validEmbedding {
			log.Warn().Str("fact_text", unit.fact.NormalizedFactText).Str("source_url", unit.sourceURL).Msg("Skipping fact storage due to invalid or missing embedding.")
			continue
		}

		log.Debug().Str("fact_text", unit.fact.NormalizedFactText).Msgf("Processing unit %d/%d for storage", i+1, len(unitsToProcess))
		storeInput := storage.StoreFactInput{
			NormalizedFactText: unit.fact.NormalizedFactText,
			Embedding:          unit.embedding.Slice(),
			OriginalQuote:      unit.fact.OriginalQuote,
			Topics:             unit.fact.Topics,
			InvolvedPeople:     unit.fact.InvolvedPeople, // This should be used to get personIDs
			SourceURL:          unit.sourceURL,
			PublishedAt:        unit.publishedDate,
		}

		// StoreFact now returns personIDs based on InvolvedPeople from the input
		factID, factSourceID, personIDs, err := deps.storer.StoreFact(ctx, storeInput)
		if err != nil {
			log.Error().Err(err).Str("fact_text", unit.fact.NormalizedFactText).Str("source_url", unit.sourceURL).Msg("Failed to store fact and its source mention")
			continue // Continue to the next fact unit
		}
		totalFactsStored++
		log.Info().Int64("fact_id", factID).Int64("fact_source_id", factSourceID).Str("fact_text", unit.fact.NormalizedFactText).Msg("Fact stored/linked successfully")

		// --- Check for Contradictions for the newly stored fact ---
		if len(personIDs) > 0 {
			// Start a new transaction for contradiction checks related to this new fact factID
			// This transaction is separate for each new fact's contradiction processing round.
			checkTx, txErr := deps.storer.Pool().Begin(ctx)
			if txErr != nil {
				log.Error().Err(txErr).Int64("fact_id", factID).Msg("Failed to begin transaction for contradiction checks. Skipping checks for this fact.")
				continue // Skip contradiction checks for this fact if transaction fails
			}
			var contradictionCheckErr error // To track errors within this transaction
			contradictionsRecordedInTx := 0

			for _, personID := range personIDs {
				log.Debug().Int64("person_id", personID).Int64("new_fact_id", factID).Msg("Checking for contradictions against existing facts for this person.")

				// Find neighbor facts for this person, excluding the fact we just inserted.
				// The embedding needed is of the current unit.fact.NormalizedFactText
				neighborFacts, err := deps.storer.FindNeighborFactsForPerson(ctx, checkTx, personID, unit.embedding.Slice(), factID)
				if err != nil {
					log.Error().Err(err).Int64("person_id", personID).Int64("fact_id", factID).Msg("Failed to find neighbor facts for contradiction check.")
					contradictionCheckErr = err
					break // Stop checking this person if neighbors can't be fetched
				}

				for _, neighbor := range neighborFacts {
					log.Debug().Int64("new_fact_id", factID).Int64("neighbor_fact_id", neighbor.ID).Int64("person_id", personID).Msg("Comparing new fact with neighbor")

					// Note: AreContradictory uses the full text of the facts.
					// unit.fact.NormalizedFactText is the text for the new fact.
					// neighbor.Text is the text for the existing fact from FindNeighborFactsForPerson result.
					isContra, reason, contraErr := deps.contradictionDetector.AreContradictory(ctx, unit.fact.NormalizedFactText, neighbor.Text)
					if contraErr != nil {
						log.Error().Err(contraErr).Int64("new_fact_id", factID).Int64("neighbor_fact_id", neighbor.ID).Msg("Error calling AreContradictory")
						contradictionCheckErr = contraErr // Record error, but continue checking other neighbors
						continue
					}

					if isContra {
						log.Warn().Int64("person_id", personID).Int64("fact_A_id", factID).Int64("fact_B_id", neighbor.ID).Str("reason", reason).Msg("Contradiction DETECTED")
						if recErr := deps.storer.RecordContradiction(ctx, checkTx, personID, factID, neighbor.ID, reason); recErr != nil {
							log.Error().Err(recErr).Int64("fact_A_id", factID).Int64("fact_B_id", neighbor.ID).Msg("Failed to record contradiction in DB")
							contradictionCheckErr = recErr // Record error
							break                          // Stop processing this person if DB recording fails
						} else {
							log.Info().Int64("fact_A_id", factID).Int64("fact_B_id", neighbor.ID).Msg("Contradiction recorded successfully.")
							contradictionsRecordedInTx++
						}
					}
				} // End loop through neighborFacts
				if contradictionCheckErr != nil {
					break
				} // If error with a person, stop processing this person's contradictions
			} // End loop through personIDs for the current new fact

			// Commit or rollback the transaction for contradiction checks for this new_fact_id
			if contradictionCheckErr != nil {
				log.Error().Err(contradictionCheckErr).Int64("fact_id", factID).Msg("Rolling back contradiction check transaction due to error.")
				if rbErr := checkTx.Rollback(ctx); rbErr != nil {
					log.Error().Err(rbErr).Int64("fact_id", factID).Msg("Error during contradiction tx rollback.")
				}
			} else if contradictionsRecordedInTx > 0 {
				log.Info().Int64("fact_id", factID).Int("contradictions_recorded", contradictionsRecordedInTx).Msg("Committing contradiction check transaction.")
				if cmErr := checkTx.Commit(ctx); cmErr != nil {
					log.Error().Err(cmErr).Int64("fact_id", factID).Msg("Error committing contradiction tx.")
					// If commit fails, the contradictions weren't actually saved.
				}
			} else {
				log.Debug().Int64("fact_id", factID).Msg("No contradictions recorded for this fact, rolling back (no-op) contradiction tx.")
				if rbErr := checkTx.Rollback(ctx); rbErr != nil { // Rollback is good practice even if no changes
					log.Error().Err(rbErr).Int64("fact_id", factID).Msg("Error during (no-op) contradiction tx rollback.")
				}
			}
		} // End if len(personIDs) > 0
	} // End loop through unitsToProcess

	log.Info().Int("total_facts_stored_this_run", totalFactsStored).Msg("Storage phase completed; contradiction checks are handled by a separate listener.")
	return nil
}

// executeCronRun is the main function executing the fact extraction workflow.
func executeCronRun(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	log.Info().Msg("Starting fact extraction pipeline run...")

	deps, err := initializeDependencies(ctx)
	if err != nil {
		log.Error().Err(err).Msg("Failed to initialize dependencies")
		return fmt.Errorf("initialization failed: %w", err)
	}
	defer deps.storer.Close()

	log.Debug().Int("search_limit_per_person", searchResultLimit).Msg("Using search limit for new URLs")

	unitsToProcess, err := collectFactUnits(ctx, deps)
	if err != nil {
		log.Error().Err(err).Msg("Error collecting fact units")
		return fmt.Errorf("failed during fact unit collection: %w", err)
	}

	if len(unitsToProcess) == 0 {
		log.Info().Msg("No new facts collected to process. Pipeline run finished.")
		return nil
	}
	log.Info().Int("count", len(unitsToProcess)).Msg("Collected fact units for further processing")

	err = generateEmbeddingsInPlace(ctx, deps, unitsToProcess)
	if err != nil {
		log.Error().Err(err).Msg("Error generating embeddings")
	}

	err = storeAndCheckContradictions(ctx, deps, unitsToProcess)
	if err != nil {
		log.Error().Err(err).Msg("Error storing facts or checking contradictions")
		return fmt.Errorf("failed during storage/contradiction check: %w", err)
	}

	log.Info().Msg("Fact extraction pipeline run completed successfully.")
	return nil
}

// Add a helper function at the package level or within the file if not already present elsewhere
func summarizedContent(s string, maxLength int) string {
	if len(s) <= maxLength {
		return s
	}
	return s[:maxLength-3] + "..."
}

// Note: The actual main function that executes the root command (e.g., rootCmd.Execute())
// is likely in another file (e.g., cmd/root.go or main.go)
