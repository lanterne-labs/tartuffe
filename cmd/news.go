package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/plugins/news"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// newsCmd represents the news command
var newsCmd = &cobra.Command{
	Use:   "news",
	Short: "Search and extract facts from news sources",
	Long:  `Search news sources for mentions of persons/parties and extract verifiable facts.`,
}

// newsSearchCmd represents the news search command
var newsSearchCmd = &cobra.Command{
	Use:   "search",
	Short: "Search news for specific targets",
	Long:  `Search news sources for mentions of specific persons or parties and extract facts.`,
	RunE:  runNewsSearch,
}

func init() {
	pluginCmd.AddCommand(newsCmd)
	newsCmd.AddCommand(newsSearchCmd)
	newsCmd.AddCommand(queueCmd)

	// Target selection
	newsSearchCmd.Flags().StringSlice("targets", nil, "specific persons/parties to search news for (by name)")
	newsSearchCmd.Flags().BoolP("force", "f", false, "force create missing persons/parties without prompting")

	// News search parameters
	newsSearchCmd.Flags().String("since", "", "only search news articles published after YYYY-MM-DD")
	newsSearchCmd.Flags().Int("max-parse", 5, "maximum news articles to parse and extract facts from per target")
	newsSearchCmd.Flags().String("location", "France", "restrict news search to this country")
	newsSearchCmd.Flags().Bool("skip-unknown", false, "skip targets with no prior news search results")
	newsSearchCmd.Flags().Bool("dry-run", false, "show what URLs would be processed without actually fetching or storing")
	newsSearchCmd.Flags().String("extractor", "webcrawler", "content extractor to use (webcrawler, firecrawl, apify)")
	newsSearchCmd.Flags().String("cache-dir", "", "directory to cache extracted content (enables caching)")
	newsSearchCmd.Flags().Bool("cache-only", false, "only load from cache, don't scrape (requires --cache-dir)")
}

func runNewsSearch(cmd *cobra.Command, args []string) error {
	// Load environment variables
	err := godotenv.Load()
	if err != nil {
		log.Warn().Msg("No .env file found, relying on environment variables")
	}

	ctx := context.Background()

	// Initialize database connection
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	// Parse flags
	targets, _ := cmd.Flags().GetStringSlice("targets")
	force, _ := cmd.Flags().GetBool("force")
	sinceStr, _ := cmd.Flags().GetString("since")
	maxParse, _ := cmd.Flags().GetInt("max-parse")
	location, _ := cmd.Flags().GetString("location")
	skipUnknown, _ := cmd.Flags().GetBool("skip-unknown")
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	extractorType, _ := cmd.Flags().GetString("extractor")
	cacheDir, _ := cmd.Flags().GetString("cache-dir")
	cacheOnly, _ := cmd.Flags().GetBool("cache-only")

	// Validate cache-only requires cache-dir
	if cacheOnly && cacheDir == "" {
		return fmt.Errorf("--cache-only requires --cache-dir to be set")
	}

	// Parse since date
	var since *time.Time
	if sinceStr != "" {
		if t, err := time.Parse("2006-01-02", sinceStr); err == nil {
			since = &t
		} else {
			log.Warn().Str("since", sinceStr).Msg("invalid --since format, expected YYYY-MM-DD")
		}
	}

	log.Info().
		Int("max_parse", maxParse).
		Str("location", location).
		Int("targets", len(targets)).
		Bool("dry_run", dryRun).
		Msg("Starting news search")

	if dryRun {
		log.Info().Msg("[DRY-RUN] Will show what would be done without fetching or storing")
	}

	// Build targets list
	var searchTargets []news.SearchTarget
	var ambiguousQueries map[string]news.TargetResolutionResult

	if len(targets) > 0 {
		// Resolve target names with fuzzy matching
		log.Info().Int("count", len(targets)).Msg("Resolving specified news targets")

		resolutions := news.ResolveTargetNames(ctx, storer, targets)
		ambiguousQueries = make(map[string]news.TargetResolutionResult)

		for query, result := range resolutions {
			if result.Error != nil {
				log.Error().Err(result.Error).Str("query", query).Msg("Failed to resolve target")
				continue
			}

			if result.IsExactMatch {
				// Exact match found
				log.Info().Str("query", query).Str("matched", result.ExactMatch.Name).Msg("Exact match found")
				searchTargets = append(searchTargets, *result.ExactMatch)
			} else if len(result.FuzzyMatches) > 0 {
				// Ambiguous - store for later
				log.Warn().Str("query", query).Int("candidates", len(result.FuzzyMatches)).Msg("Ambiguous name - disambiguation needed")
				ambiguousQueries[query] = result
			} else {
				// No matches found - also needs disambiguation (user can create)
				log.Warn().Str("query", query).Msg("No matches found in database - user can create")
				ambiguousQueries[query] = result
			}
		}

		// Handle disambiguation
		if len(ambiguousQueries) > 0 {
			var resolvedTargets []string
			var err error

			if force {
				// Force mode: auto-create missing persons without prompting
				resolvedTargets, err = handleDisambiguationForce(ctx, storer, ambiguousQueries)
			} else {
				// Interactive mode: prompt user
				resolvedTargets, err = handleDisambiguation(ctx, storer, ambiguousQueries)
			}

			if err != nil {
				return err
			}

			// Re-resolve the resolved names
			for _, target := range resolvedTargets {
				result := news.ResolveTargetName(ctx, storer, target)
				if result.IsExactMatch {
					searchTargets = append(searchTargets, *result.ExactMatch)
				}
			}
		}
	} else {
		// Get all persons from database
		log.Info().Msg("Fetching all persons from database for news search")
		allPersons, err := storer.GetAllPersons(ctx)
		if err != nil {
			return fmt.Errorf("failed to fetch persons: %w", err)
		}

		if len(allPersons) == 0 {
			log.Info().Msg("No persons in database to search news for")
			return nil
		}

		for _, p := range allPersons {
			targetType := news.TargetPerson
			if p.EntityType == "party" {
				targetType = news.TargetParty
			}

			personID := p.ID
			searchTargets = append(searchTargets, news.SearchTarget{
				Name:     p.Name,
				Type:     targetType,
				PersonID: &personID,
			})
		}

		log.Info().Int("count", len(searchTargets)).Msg("Loaded persons from database")
	}

	if len(searchTargets) == 0 {
		log.Info().Msg("No targets for news search")
		return nil
	}

	// Initialize SerpAPI provider
	serpAPI, err := search.NewSerpAPINewsProviderFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize SerpAPI: %w", err)
	}

	// Initialize shared components (not needed in dry-run mode)
	var factExtractor *categorizer.OpenAIExtractor
	var embedder *embedding.OpenAIEmbeddingGenerator
	var rawExtractor extractor.ContentExtractor

	if !dryRun {
		// Initialize fact extractor (shared across targets)
		factExtractor, err = categorizer.NewOpenAIExtractorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to initialize fact extractor: %w", err)
		}

		// Initialize embedder (shared across targets)
		embedder, err = embedding.NewOpenAIEmbeddingGeneratorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to initialize embedder: %w", err)
		}

		// Initialize raw content extractor (without caching - will be wrapped per-target)
		if !cacheOnly {
			rawExtractor, err = extractor.NewExtractorFromType(extractorType)
			if err != nil {
				return fmt.Errorf("failed to initialize content extractor: %w", err)
			}
		}
	}

	// Set up search options
	opts := news.Options{
		DateFrom:     since,
		MaxParse:     maxParse,
		Location:     location,
		RevisitHours: 24 * 365 * 10, // Never re-fetch same URL (10 years)
		DryRun:       dryRun,
	}

	// Prioritize targets
	prioritizeOpts := news.PrioritizationOptions{
		PartiesFirst:  true,
		SkipNoResults: skipUnknown,
		LookbackDays:  30,
	}

	prioritizedTargets, err := news.PrioritizeTargets(ctx, storer, searchTargets, prioritizeOpts)
	if err != nil {
		return fmt.Errorf("failed to prioritize targets: %w", err)
	}

	log.Info().Int("targets", len(prioritizedTargets)).Msg("Prioritized targets for news search")

	// Cache-only mode: skip news search entirely and process cached files
	if cacheOnly {
		return runCacheOnlyExtraction(ctx, prioritizedTargets, cacheDir, factExtractor, embedder, storer)
	}

	// Search news for each target
	totalFacts := 0
	totalSkipped := 0
	for i, target := range prioritizedTargets {
		log.Info().
			Int("progress", i+1).
			Int("total", len(prioritizedTargets)).
			Str("target", target.Name).
			Str("type", string(target.Type)).
			Msg("Searching news for target")

		// Create news provider for this target
		var newsProvider *news.Provider
		if dryRun {
			newsProvider = &news.Provider{
				NewsSearch: serpAPI,
				Storer:     storer,
			}
		} else {
			// Create content extractor with target-specific caching
			var contentExt extractor.ContentExtractor
			if cacheDir != "" {
				// Caching enabled: write to target-specific subdirectory
				targetCacheDir := getTargetCacheDir(cacheDir, target.Name)
				contentExt = extractor.NewCacheExtractor(rawExtractor, targetCacheDir, true, true)
				log.Debug().Str("target", target.Name).Str("cache_dir", targetCacheDir).Msg("Caching enabled")
			} else {
				// No caching
				contentExt = rawExtractor
			}

			// Wrap with batch extractor for concurrency control
			batchExtractor := core.NewBatchExtractorFromEnv(contentExt)

			newsProvider = &news.Provider{
				NewsSearch:       serpAPI,
				ContentExtractor: batchExtractor,
				FactExtractor:    factExtractor,
				Embedder:         embedder,
				Storer:           storer,
			}
		}

		result, err := newsProvider.CollectFacts(ctx, target, opts)
		if err != nil {
			log.Error().Err(err).Str("target", target.Name).Msg("Failed to collect facts for target")
			continue
		}

		log.Info().
			Str("target", target.Name).
			Int("articles_found", result.ArticlesFound).
			Int("articles_parsed", result.ArticlesParsed).
			Int("facts_extracted", result.FactsExtracted).
			Int("skipped_already_processed", result.SkippedAlreadyProcessed).
			Msg("Completed news search for target")

		totalFacts += result.FactsExtracted
		totalSkipped += result.SkippedAlreadyProcessed
	}

	log.Info().
		Int("total_targets", len(prioritizedTargets)).
		Int("total_facts", totalFacts).
		Int("total_skipped_already_processed", totalSkipped).
		Msg("News search completed successfully")

	return nil
}

// getTargetCacheDir returns the target-specific cache subdirectory.
// Creates the directory if it doesn't exist.
func getTargetCacheDir(baseDir string, targetName string) string {
	normalizedName := util.NormalizeToDirName(targetName)
	targetDir := filepath.Join(baseDir, normalizedName)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		log.Warn().Err(err).Str("dir", targetDir).Msg("Failed to create target cache directory")
		return baseDir // Fallback to base directory
	}

	return targetDir
}

// handleDisambiguationForce automatically creates missing persons without prompting
func handleDisambiguationForce(ctx context.Context, storer *storage.PostgresStorer, ambiguous map[string]news.TargetResolutionResult) ([]string, error) {
	resolvedTargets := make([]string, 0)
	const forceThreshold = 0.8 // Only auto-use matches with >80% similarity

	for query, result := range ambiguous {
		// Case 1: No matches found - auto-create as human
		if len(result.FuzzyMatches) == 0 {
			log.Info().Str("query", query).Msg("Auto-creating person (force mode)")

			id, inserted, err := storer.AddPersonWithType(ctx, query, "human")
			if err != nil {
				log.Error().Err(err).Str("query", query).Msg("Failed to create person")
				continue
			}

			if inserted {
				log.Info().Str("name", query).Int64("id", id).Msg("Created new person")
			} else {
				log.Info().Str("name", query).Int64("id", id).Msg("Person already exists")
			}

			resolvedTargets = append(resolvedTargets, query)
			continue
		}

		// Case 2: Check if top match is good enough (>80% similarity)
		topMatch := result.FuzzyMatches[0]
		if topMatch.Score >= forceThreshold {
			log.Info().
				Str("query", query).
				Str("matched", topMatch.Target.Name).
				Float64("score", topMatch.Score).
				Msg("Auto-using high-confidence fuzzy match (force mode)")
			resolvedTargets = append(resolvedTargets, topMatch.Target.Name)
			continue
		}

		// Case 3: Low-confidence matches - create new person instead
		log.Info().
			Str("query", query).
			Float64("top_score", topMatch.Score).
			Msg("Low-confidence matches, auto-creating new person (force mode)")

		id, inserted, err := storer.AddPersonWithType(ctx, query, "human")
		if err != nil {
			log.Error().Err(err).Str("query", query).Msg("Failed to create person")
			continue
		}

		if inserted {
			log.Info().Str("name", query).Int64("id", id).Msg("Created new person")
		} else {
			log.Info().Str("name", query).Int64("id", id).Msg("Person already exists")
		}

		resolvedTargets = append(resolvedTargets, query)
	}

	if len(resolvedTargets) == 0 {
		return nil, fmt.Errorf("no targets resolved in force mode")
	}

	return resolvedTargets, nil
}

// handleDisambiguation prompts the user to disambiguate or create missing targets
func handleDisambiguation(ctx context.Context, storer *storage.PostgresStorer, ambiguous map[string]news.TargetResolutionResult) ([]string, error) {
	fmt.Println("\nSome target names need disambiguation or don't exist yet:")
	fmt.Println()

	resolvedTargets := make([]string, 0)

	for query, result := range ambiguous {
		fmt.Printf("Query: %q\n", query)

		// Case 1: No matches found - offer to create
		if len(result.FuzzyMatches) == 0 {
			fmt.Printf("No matches found in database.\n")
			fmt.Printf("Create this person/party? (y=person, p=party, n=skip): ")

			reader := bufio.NewReader(os.Stdin)
			input, _ := reader.ReadString('\n')
			input = strings.TrimSpace(strings.ToLower(input))

			if input == "y" || input == "p" {
				entityType := "human"
				if input == "p" {
					entityType = "party"
				}

				id, inserted, err := storer.AddPersonWithType(ctx, query, entityType)
				if err != nil {
					fmt.Printf("Failed to create %s: %v\n", entityType, err)
					continue
				}

				if inserted {
					fmt.Printf("Created new %s: %s (ID: %d)\n\n", entityType, query, id)
				} else {
					fmt.Printf("Person already exists (ID: %d)\n\n", id)
				}

				resolvedTargets = append(resolvedTargets, query)
			} else {
				fmt.Println("Skipping target")
			}
			continue
		}

		// Case 2: Multiple matches - ask user to choose
		fmt.Printf("Found %d potential matches:\n", len(result.FuzzyMatches))

		for i, match := range result.FuzzyMatches {
			fmt.Printf("  %d) %s (%s) - similarity: %.2f\n",
				i+1, match.Target.Name, match.Target.Type, match.Score)
		}
		fmt.Printf("  0) Skip this target\n")
		fmt.Print("Enter your choice (0-" + fmt.Sprintf("%d", len(result.FuzzyMatches)) + "): ")

		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		choice, err := strconv.Atoi(input)
		if err != nil || choice < 0 || choice > len(result.FuzzyMatches) {
			fmt.Println("Invalid choice, skipping this target")
			continue
		}

		if choice == 0 {
			fmt.Println("Skipping target")
			continue
		}

		// Add the selected match
		selected := result.FuzzyMatches[choice-1]
		resolvedTargets = append(resolvedTargets, selected.Target.Name)
		fmt.Printf("Selected: %s\n\n", selected.Target.Name)
	}

	if len(resolvedTargets) == 0 {
		fmt.Println("No targets selected. Exiting.")
		return nil, fmt.Errorf("no targets selected")
	}

	return resolvedTargets, nil
}

// runCacheOnlyExtraction processes cached files without doing a news search.
// This loads all JSON files from the target's cache directory and extracts facts.
func runCacheOnlyExtraction(
	ctx context.Context,
	targets []news.SearchTarget,
	cacheDir string,
	factExtractor *categorizer.OpenAIExtractor,
	embedder *embedding.OpenAIEmbeddingGenerator,
	storer *storage.PostgresStorer,
) error {
	log.Info().
		Int("targets", len(targets)).
		Str("cache_dir", cacheDir).
		Msg("Starting cache-only extraction (no news search)")

	factInserter := core.NewFactInserter(storer)
	totalFacts := 0
	totalFiles := 0

	for i, target := range targets {
		log.Info().
			Int("progress", i+1).
			Int("total", len(targets)).
			Str("target", target.Name).
			Str("type", string(target.Type)).
			Msg("Processing cached content for target")

		// Get target-specific cache directory
		targetCacheDir := getTargetCacheDir(cacheDir, target.Name)
		cacheExt := extractor.NewCacheOnlyExtractor(targetCacheDir)

		// Load all cached files
		cachedContents, err := cacheExt.LoadAllCached()
		if err != nil {
			log.Error().Err(err).Str("target", target.Name).Str("cache_dir", targetCacheDir).Msg("Failed to load cached content")
			continue
		}

		if len(cachedContents) == 0 {
			log.Warn().Str("target", target.Name).Str("cache_dir", targetCacheDir).Msg("No cached files found")
			continue
		}

		log.Info().
			Str("target", target.Name).
			Int("cached_files", len(cachedContents)).
			Msg("Loaded cached content")

		targetFacts := 0
		for _, content := range cachedContents {
			if content == nil || strings.TrimSpace(content.Content) == "" {
				log.Debug().Str("url", content.URL).Msg("Skipping empty cached content")
				continue
			}

			// Extract facts
			facts, err := factExtractor.ExtractFactsWithType(ctx, target.Name, string(target.Type), *content)
			if err != nil {
				log.Warn().Err(err).Str("url", content.URL).Msg("Failed to extract facts from cached content")
				continue
			}

			if len(facts) == 0 {
				log.Debug().Str("url", content.URL).Msg("No facts extracted from cached content")
				continue
			}

			// Generate embeddings and store facts
			for _, fact := range facts {
				embeddings, err := embedder.GenerateEmbeddings(ctx, []string{fact.EmbeddingText})
				if err != nil || len(embeddings) == 0 || len(embeddings[0]) == 0 {
					log.Warn().Err(err).Str("fact", truncateString(fact.NormalizedFactText, 80)).Msg("Failed to generate embedding, skipping fact")
					continue
				}

				input := core.StoreFactInput{
					SubjectEntity:      fact.SubjectEntity,
					SubjectEntityType:  fact.SubjectEntityType,
					NormalizedFactText: fact.NormalizedFactText,
					EmbeddingText:      fact.EmbeddingText,
					Embedding:          embeddings[0],
					OriginalQuote:      fact.OriginalQuote,
					Topics:             fact.Topics,
					InvolvedPeople:     fact.InvolvedPeople,
					SourceURL:          content.URL,
					PublishedAt:        content.DatePublished,
					SourceType:         "news",
					Metadata: map[string]any{
						"target_type":  string(target.Type),
						"search_query": target.Name,
						"cache_only":   true,
					},
				}

				err = factInserter.InsertFacts(ctx, []core.StoreFactInput{input})
				if err != nil {
					log.Warn().Err(err).Str("fact", truncateString(fact.NormalizedFactText, 80)).Msg("Failed to store fact")
					continue
				}

				targetFacts++
			}

			totalFiles++
		}

		log.Info().
			Str("target", target.Name).
			Int("files_processed", len(cachedContents)).
			Int("facts_extracted", targetFacts).
			Msg("Completed cache-only extraction for target")

		totalFacts += targetFacts
	}

	log.Info().
		Int("total_targets", len(targets)).
		Int("total_files", totalFiles).
		Int("total_facts", totalFacts).
		Msg("Cache-only extraction completed")

	return nil
}

// truncateString truncates a string to maxLen characters, adding "..." if truncated.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
