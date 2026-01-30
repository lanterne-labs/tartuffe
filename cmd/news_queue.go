package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
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

// queueCmd represents the queue command group
var queueCmd = &cobra.Command{
	Use:   "queue",
	Short: "Manage the news search queue",
	Long:  `Add, remove, list, and process persons in the news search queue.`,
}

// queueAddCmd adds a person to the news queue
var queueAddCmd = &cobra.Command{
	Use:   "add [person-name]",
	Short: "Add a person to the news search queue",
	Long:  `Add a person to the news search queue. The person must already exist in the database.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runQueueAdd,
}

// queueRemoveCmd removes a person from the news queue
var queueRemoveCmd = &cobra.Command{
	Use:   "remove [person-name]",
	Short: "Remove a person from the news search queue",
	Long:  `Remove a person from the news search queue by name.`,
	Args:  cobra.ExactArgs(1),
	RunE:  runQueueRemove,
}

// queueListCmd lists all persons in the news queue
var queueListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all persons in the news search queue",
	Long:  `List all persons currently in the news search queue, ordered by person ID.`,
	Args:  cobra.NoArgs,
	RunE:  runQueueList,
}

// queueProcessCmd processes the news queue
var queueProcessCmd = &cobra.Command{
	Use:   "process",
	Short: "Process the news search queue",
	Long:  `Process persons in the news search queue, fetching news articles and extracting facts.`,
	Args:  cobra.NoArgs,
	RunE:  runQueueProcess,
}

func init() {
	// Add queue subcommands
	queueCmd.AddCommand(queueAddCmd)
	queueCmd.AddCommand(queueRemoveCmd)
	queueCmd.AddCommand(queueListCmd)
	queueCmd.AddCommand(queueProcessCmd)

	// Process command flags
	queueProcessCmd.Flags().Int("limit", 0, "maximum number of persons to process (0 = all)")
	queueProcessCmd.Flags().Int("max-parse", 5, "maximum news articles to parse per person")
	queueProcessCmd.Flags().String("since", "", "only search news articles published after YYYY-MM-DD")
	queueProcessCmd.Flags().Int("days", 0, "only search news from last N days (alternative to --since)")
	queueProcessCmd.Flags().String("location", "France", "restrict news search to this country")
	queueProcessCmd.Flags().Bool("skip-recent", true, "skip persons searched recently (uses news_search_log)")
	queueProcessCmd.Flags().Int("lookback-days", 30, "days to look back when checking if person was recently searched")
	queueProcessCmd.Flags().String("extractor", "webcrawler", "content extractor to use (webcrawler, firecrawl, apify)")
	queueProcessCmd.Flags().String("cache-dir", "", "directory to cache extracted content (enables caching)")
	queueProcessCmd.Flags().Bool("cache-only", false, "only load from cache, don't scrape (requires --cache-dir)")
}

func runQueueAdd(cmd *cobra.Command, args []string) error {
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

	personName := args[0]

	// Resolve name using fuzzy matching
	result := news.ResolveTargetName(ctx, storer, personName)

	if result.Error != nil {
		return fmt.Errorf("failed to resolve person name: %w", result.Error)
	}

	var personID int64
	var resolvedName string

	if result.IsExactMatch {
		// Exact match found
		personID = *result.ExactMatch.PersonID
		resolvedName = result.ExactMatch.Name
		log.Info().Str("query", personName).Str("matched", resolvedName).Msg("Exact match found")
	} else if len(result.FuzzyMatches) > 0 {
		// Multiple fuzzy matches - ask user to disambiguate
		fmt.Printf("Query: %q\n", personName)
		fmt.Printf("Found %d potential matches:\n", len(result.FuzzyMatches))

		for i, match := range result.FuzzyMatches {
			fmt.Printf("  %d) %s (%s) - similarity: %.2f\n",
				i+1, match.Target.Name, match.Target.Type, match.Score)
		}
		fmt.Printf("  0) Cancel\n")
		fmt.Print("Enter your choice (0-" + fmt.Sprintf("%d", len(result.FuzzyMatches)) + "): ")

		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		choice, err := strconv.Atoi(input)
		if err != nil || choice < 0 || choice > len(result.FuzzyMatches) {
			return fmt.Errorf("invalid choice")
		}

		if choice == 0 {
			fmt.Println("Cancelled")
			return nil
		}

		// Use selected match
		selected := result.FuzzyMatches[choice-1]
		personID = *selected.Target.PersonID
		resolvedName = selected.Target.Name
		fmt.Printf("Selected: %s\n", resolvedName)
	} else {
		// No matches found
		return fmt.Errorf("person %q not found in database. Use 'persons add' to create first", personName)
	}

	// Add to queue
	err = storer.AddToNewsQueue(ctx, personID)
	if err != nil {
		return fmt.Errorf("failed to add to queue: %w", err)
	}

	// Check if it was already in queue
	inQueue, _ := storer.IsInNewsQueue(ctx, personID)
	if inQueue {
		fmt.Printf("Added %s (ID: %d) to news queue\n", resolvedName, personID)
	}

	return nil
}

func runQueueRemove(cmd *cobra.Command, args []string) error {
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

	personName := args[0]

	// Resolve name using fuzzy matching
	result := news.ResolveTargetName(ctx, storer, personName)

	if result.Error != nil {
		return fmt.Errorf("failed to resolve person name: %w", result.Error)
	}

	var personID int64
	var resolvedName string

	if result.IsExactMatch {
		// Exact match found
		personID = *result.ExactMatch.PersonID
		resolvedName = result.ExactMatch.Name
		log.Info().Str("query", personName).Str("matched", resolvedName).Msg("Exact match found")
	} else if len(result.FuzzyMatches) > 0 {
		// Multiple fuzzy matches - ask user to disambiguate
		fmt.Printf("Query: %q\n", personName)
		fmt.Printf("Found %d potential matches:\n", len(result.FuzzyMatches))

		for i, match := range result.FuzzyMatches {
			fmt.Printf("  %d) %s (%s) - similarity: %.2f\n",
				i+1, match.Target.Name, match.Target.Type, match.Score)
		}
		fmt.Printf("  0) Cancel\n")
		fmt.Print("Enter your choice (0-" + fmt.Sprintf("%d", len(result.FuzzyMatches)) + "): ")

		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		input = strings.TrimSpace(input)

		choice, err := strconv.Atoi(input)
		if err != nil || choice < 0 || choice > len(result.FuzzyMatches) {
			return fmt.Errorf("invalid choice")
		}

		if choice == 0 {
			fmt.Println("Cancelled")
			return nil
		}

		// Use selected match
		selected := result.FuzzyMatches[choice-1]
		personID = *selected.Target.PersonID
		resolvedName = selected.Target.Name
		fmt.Printf("Selected: %s\n", resolvedName)
	} else {
		// No matches found
		return fmt.Errorf("person %q not found in database", personName)
	}

	// Remove from queue
	err = storer.RemoveFromNewsQueue(ctx, personID)
	if err != nil {
		return err
	}

	fmt.Printf("Removed %s (ID: %d) from news queue\n", resolvedName, personID)
	return nil
}

func runQueueList(cmd *cobra.Command, args []string) error {
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

	// Get queue
	queue, err := storer.GetNewsQueue(ctx)
	if err != nil {
		return fmt.Errorf("failed to get news queue: %w", err)
	}

	if len(queue) == 0 {
		fmt.Println("News queue is empty")
		return nil
	}

	// Display queue in table format
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PERSON ID\tNAME\tTYPE\tADDED AT")

	for _, entry := range queue {
		fmt.Fprintf(w, "%d\t%s\t%s\t%s\n",
			entry.PersonID,
			entry.Name,
			entry.EntityType,
			entry.AddedAt.Format("2006-01-02 15:04"))
	}

	w.Flush()

	fmt.Printf("\nTotal: %d persons in queue\n", len(queue))
	return nil
}

func runQueueProcess(cmd *cobra.Command, args []string) error {
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
	limit, _ := cmd.Flags().GetInt("limit")
	maxParse, _ := cmd.Flags().GetInt("max-parse")
	sinceStr, _ := cmd.Flags().GetString("since")
	days, _ := cmd.Flags().GetInt("days")
	location, _ := cmd.Flags().GetString("location")
	skipRecent, _ := cmd.Flags().GetBool("skip-recent")
	lookbackDays, _ := cmd.Flags().GetInt("lookback-days")
	extractorType, _ := cmd.Flags().GetString("extractor")
	cacheDir, _ := cmd.Flags().GetString("cache-dir")
	cacheOnly, _ := cmd.Flags().GetBool("cache-only")

	// Validate cache-only requires cache-dir
	if cacheOnly && cacheDir == "" {
		return fmt.Errorf("--cache-only requires --cache-dir to be set")
	}

	// Validate --since and --days are mutually exclusive
	if sinceStr != "" && days > 0 {
		return fmt.Errorf("--since and --days are mutually exclusive, use only one")
	}

	// Parse since date
	var since *time.Time
	if sinceStr != "" {
		t, err := time.Parse("2006-01-02", sinceStr)
		if err != nil {
			return fmt.Errorf("invalid --since format (expected YYYY-MM-DD): %w", err)
		}
		since = &t
	} else if days > 0 {
		t := time.Now().AddDate(0, 0, -days)
		since = &t
		log.Info().Int("days", days).Time("since", t).Msg("Calculated since date from --days flag")
	}

	// Get queue
	queue, err := storer.GetNewsQueue(ctx)
	if err != nil {
		return fmt.Errorf("failed to get news queue: %w", err)
	}

	if len(queue) == 0 {
		log.Info().Msg("News queue is empty")
		fmt.Println("News queue is empty")
		return nil
	}

	log.Info().Int("queue_size", len(queue)).Msg("Loaded news queue")

	// Apply limit
	if limit > 0 && len(queue) > limit {
		queue = queue[:limit]
		log.Info().Int("limited_to", limit).Msg("Applied limit to queue")
	}

	// Filter by skip-recent flag
	if skipRecent {
		filtered := make([]storage.QueueEntry, 0)
		for _, entry := range queue {
			shouldSearch, err := storer.ShouldSearchNews(ctx, entry.PersonID, lookbackDays)
			if err != nil {
				log.Warn().Err(err).Int64("person_id", entry.PersonID).Msg("Failed to check search history")
				continue
			}
			if shouldSearch {
				filtered = append(filtered, entry)
			} else {
				log.Debug().Str("name", entry.Name).Msg("Skipping recently searched person")
			}
		}
		queue = filtered
		log.Info().Int("filtered_count", len(queue)).Msg("Filtered queue by recent searches")
	}

	if len(queue) == 0 {
		log.Info().Msg("No persons to process after filtering")
		fmt.Println("No persons to process (all were recently searched)")
		return nil
	}

	// Convert to SearchTargets
	targets := make([]news.SearchTarget, len(queue))
	for i, entry := range queue {
		targetType := news.TargetPerson
		if entry.EntityType == "party" {
			targetType = news.TargetParty
		}
		targets[i] = news.SearchTarget{
			Name:     entry.Name,
			Type:     targetType,
			PersonID: &entry.PersonID,
		}
	}

	// Initialize SerpAPI provider
	serpAPI, err := search.NewSerpAPINewsProviderFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize SerpAPI: %w", err)
	}

	// Initialize shared components
	factExtractor, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize fact extractor: %w", err)
	}

	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize embedder: %w", err)
	}

	// Initialize raw content extractor (without caching - will be wrapped per-target)
	var rawExtractor extractor.ContentExtractor
	if !cacheOnly {
		rawExtractor, err = extractor.NewExtractorFromType(extractorType)
		if err != nil {
			return fmt.Errorf("failed to initialize content extractor: %w", err)
		}
	}

	// Set up search options
	opts := news.Options{
		DateFrom:     since,
		MaxParse:     maxParse,
		Location:     location,
		RevisitHours: 24 * 365 * 10, // Never re-fetch same URL (10 years)
	}

	log.Info().
		Int("targets", len(targets)).
		Int("max_parse", maxParse).
		Str("location", location).
		Msg("Starting news queue processing")

	// Process each target
	totalFacts := 0
	successCount := 0
	errorCount := 0

	for i, target := range targets {
		log.Info().
			Int("progress", i+1).
			Int("total", len(targets)).
			Str("target", target.Name).
			Str("type", string(target.Type)).
			Msg("Processing queue entry")

		// Create content extractor with target-specific caching
		var contentExt extractor.ContentExtractor
		if cacheOnly {
			// Cache-only mode: read from target-specific subdirectory
			targetCacheDir := getQueueTargetCacheDir(cacheDir, target.Name)
			contentExt = extractor.NewCacheOnlyExtractor(targetCacheDir)
			log.Debug().Str("target", target.Name).Str("cache_dir", targetCacheDir).Msg("Using cache-only mode")
		} else if cacheDir != "" {
			// Caching enabled: write to target-specific subdirectory
			targetCacheDir := getQueueTargetCacheDir(cacheDir, target.Name)
			contentExt = extractor.NewCacheExtractor(rawExtractor, targetCacheDir, true, true)
			log.Debug().Str("target", target.Name).Str("cache_dir", targetCacheDir).Msg("Caching enabled")
		} else {
			// No caching
			contentExt = rawExtractor
		}

		// Wrap with batch extractor for concurrency control
		batchExtractor := core.NewBatchExtractorFromEnv(contentExt)

		// Create news provider for this target
		newsProvider := &news.Provider{
			NewsSearch:       serpAPI,
			ContentExtractor: batchExtractor,
			FactExtractor:    factExtractor,
			Embedder:         embedder,
			Storer:           storer,
		}

		result, err := newsProvider.CollectFacts(ctx, target, opts)
		if err != nil {
			log.Error().Err(err).Str("target", target.Name).Msg("Failed to collect facts")
			errorCount++
			continue
		}

		successCount++
		totalFacts += result.FactsExtracted

		log.Info().
			Str("target", target.Name).
			Int("articles_found", result.ArticlesFound).
			Int("articles_parsed", result.ArticlesParsed).
			Int("facts_extracted", result.FactsExtracted).
			Msg("Completed processing")

		fmt.Printf("[%d/%d] %s: %d articles found, %d parsed, %d facts extracted\n",
			i+1, len(targets), target.Name,
			result.ArticlesFound, result.ArticlesParsed, result.FactsExtracted)
	}

	log.Info().
		Int("total_processed", successCount).
		Int("total_errors", errorCount).
		Int("total_facts", totalFacts).
		Msg("Queue processing completed")

	fmt.Printf("\nProcessing complete:\n")
	fmt.Printf("  Successful: %d\n", successCount)
	fmt.Printf("  Errors: %d\n", errorCount)
	fmt.Printf("  Total facts extracted: %d\n", totalFacts)

	return nil
}

// getQueueTargetCacheDir returns the target-specific cache subdirectory.
// Creates the directory if it doesn't exist.
func getQueueTargetCacheDir(baseDir string, targetName string) string {
	normalizedName := util.NormalizeToDirName(targetName)
	targetDir := filepath.Join(baseDir, normalizedName)

	// Create directory if it doesn't exist
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		log.Warn().Err(err).Str("dir", targetDir).Msg("Failed to create target cache directory")
		return baseDir // Fallback to base directory
	}

	return targetDir
}
