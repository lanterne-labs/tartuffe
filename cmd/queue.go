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
	"digital-mirror/backend/plugins/twitter"
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
	Short: "Manage the search queue",
	Long:  `Add, remove, list, and process persons in the search queue. The search queue is shared across all plugins (news, twitter).`,
}

// queueAddCmd adds a person to the search queue
var queueAddCmd = &cobra.Command{
	Use:         "add [person-name]",
	Short:       "Add a person to the search queue",
	Long:        `Add a person to the search queue. The person must already exist in the database.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runQueueAdd,
}

// queueRemoveCmd removes a person from the search queue
var queueRemoveCmd = &cobra.Command{
	Use:         "remove [person-name]",
	Short:       "Remove a person from the search queue",
	Long:        `Remove a person from the search queue by name.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.ExactArgs(1),
	RunE:        runQueueRemove,
}

// queueListCmd lists all persons in the search queue
var queueListCmd = &cobra.Command{
	Use:         "list",
	Short:       "List all persons in the search queue",
	Long:        `List all persons currently in the search queue, ordered by person ID.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runQueueList,
}

// queueProcessCmd processes the search queue
var queueProcessCmd = &cobra.Command{
	Use:         "process",
	Short:       "Process the search queue with all plugins",
	Long:        `Process persons in the search queue, running selected plugins (news, twitter) to extract facts.`,
	Annotations: map[string]string{"needs_db": "true"},
	Args:        cobra.NoArgs,
	RunE:        runQueueProcess,
}

func init() {
	rootCmd.AddCommand(queueCmd)

	// Add queue subcommands
	queueCmd.AddCommand(queueAddCmd)
	queueCmd.AddCommand(queueRemoveCmd)
	queueCmd.AddCommand(queueListCmd)
	queueCmd.AddCommand(queueProcessCmd)

	// Shared process flags
	queueProcessCmd.Flags().Int("limit", 0, "maximum number of persons to process (0 = all)")
	queueProcessCmd.Flags().String("since", "", "only search content published after YYYY-MM-DD")
	queueProcessCmd.Flags().Int("days", 0, "only search content from last N days (alternative to --since)")
	queueProcessCmd.Flags().Int("window-days", 0, "break date range into windows of N days (requires --since or --days, 0=disabled)")

	// Plugin selection
	queueProcessCmd.Flags().String("plugins", "news,twitter", "comma-separated list of plugins to run (news, twitter)")

	// News-specific flags
	queueProcessCmd.Flags().Int("news-max-parse", 5, "maximum news articles to parse per person")
	queueProcessCmd.Flags().String("news-extractor", "webcrawler", "content extractor to use (webcrawler, firecrawl, apify)")
	queueProcessCmd.Flags().String("news-cache-dir", "", "directory to cache extracted content (enables caching)")
	queueProcessCmd.Flags().Bool("news-cache-only", false, "only load from cache, don't scrape (requires --news-cache-dir)")
	queueProcessCmd.Flags().Int("news-max-per-story", 0, "max articles per story_token (0=no cap)")
	queueProcessCmd.Flags().String("news-location", "France", "restrict news search to this country")

	// Twitter-specific flags
	queueProcessCmd.Flags().Int("twitter-max-tweets", 50, "maximum tweets to fetch per handle")
	queueProcessCmd.Flags().Int("twitter-batch-size", 20, "tweets per LLM extraction call")
	queueProcessCmd.Flags().Bool("twitter-force", false, "bypass dedup, re-fetch and re-process everything")
	queueProcessCmd.Flags().Bool("twitter-dry-run", false, "show what would be done without fetching or storing")
}

func runQueueAdd(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	personName := args[0]

	// Resolve name using fuzzy matching
	result := core.ResolveTargetName(ctx, storer, personName)

	if result.Error != nil {
		return fmt.Errorf("failed to resolve person name: %w", result.Error)
	}

	var personID int64
	var resolvedName string

	if result.IsExactMatch {
		personID = *result.ExactMatch.PersonID
		resolvedName = result.ExactMatch.Name
		log.Info().Str("query", personName).Str("matched", resolvedName).Msg("Exact match found")
	} else if len(result.FuzzyMatches) > 0 {
		// Multiple fuzzy matches - ask user to disambiguate
		fmt.Printf("Query: %q\n", personName)
		fmt.Printf("Found %d potential matches:\n", len(result.FuzzyMatches))

		for i, match := range result.FuzzyMatches {
			fmt.Printf("  %d) %s (%s) - similarity: %.2f\n",
				i+1, match.Target.Name, match.Target.EntityType, match.Score)
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

		selected := result.FuzzyMatches[choice-1]
		personID = *selected.Target.PersonID
		resolvedName = selected.Target.Name
		fmt.Printf("Selected: %s\n", resolvedName)
	} else {
		return fmt.Errorf("person %q not found in database. Use 'persons add' to create first", personName)
	}

	// Add to queue
	err = storer.AddToSearchQueue(ctx, personID)
	if err != nil {
		return fmt.Errorf("failed to add to queue: %w", err)
	}

	// Check if it was already in queue
	inQueue, _ := storer.IsInSearchQueue(ctx, personID)
	if inQueue {
		fmt.Printf("Added %s (ID: %d) to search queue\n", resolvedName, personID)
	}

	return nil
}

func runQueueRemove(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	personName := args[0]

	// Resolve name using fuzzy matching
	result := core.ResolveTargetName(ctx, storer, personName)

	if result.Error != nil {
		return fmt.Errorf("failed to resolve person name: %w", result.Error)
	}

	var personID int64
	var resolvedName string

	if result.IsExactMatch {
		personID = *result.ExactMatch.PersonID
		resolvedName = result.ExactMatch.Name
		log.Info().Str("query", personName).Str("matched", resolvedName).Msg("Exact match found")
	} else if len(result.FuzzyMatches) > 0 {
		// Multiple fuzzy matches - ask user to disambiguate
		fmt.Printf("Query: %q\n", personName)
		fmt.Printf("Found %d potential matches:\n", len(result.FuzzyMatches))

		for i, match := range result.FuzzyMatches {
			fmt.Printf("  %d) %s (%s) - similarity: %.2f\n",
				i+1, match.Target.Name, match.Target.EntityType, match.Score)
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

		selected := result.FuzzyMatches[choice-1]
		personID = *selected.Target.PersonID
		resolvedName = selected.Target.Name
		fmt.Printf("Selected: %s\n", resolvedName)
	} else {
		return fmt.Errorf("person %q not found in database", personName)
	}

	// Remove from queue
	err = storer.RemoveFromSearchQueue(ctx, personID)
	if err != nil {
		return err
	}

	fmt.Printf("Removed %s (ID: %d) from search queue\n", resolvedName, personID)
	return nil
}

func runQueueList(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	queue, err := storer.GetSearchQueue(ctx)
	if err != nil {
		return fmt.Errorf("failed to get search queue: %w", err)
	}

	if len(queue) == 0 {
		fmt.Println("Search queue is empty")
		return nil
	}

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
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	// Parse shared flags
	limit, _ := cmd.Flags().GetInt("limit")
	sinceStr, _ := cmd.Flags().GetString("since")
	days, _ := cmd.Flags().GetInt("days")
	windowDays, _ := cmd.Flags().GetInt("window-days")
	pluginsStr, _ := cmd.Flags().GetString("plugins")

	// News-specific flags
	newsMaxParse, _ := cmd.Flags().GetInt("news-max-parse")
	newsExtractorType, _ := cmd.Flags().GetString("news-extractor")
	newsCacheDir, _ := cmd.Flags().GetString("news-cache-dir")
	newsCacheOnly, _ := cmd.Flags().GetBool("news-cache-only")
	newsMaxPerStory, _ := cmd.Flags().GetInt("news-max-per-story")
	newsLocation, _ := cmd.Flags().GetString("news-location")

	// Twitter-specific flags
	twitterMaxTweets, _ := cmd.Flags().GetInt("twitter-max-tweets")
	twitterBatchSize, _ := cmd.Flags().GetInt("twitter-batch-size")
	twitterForce, _ := cmd.Flags().GetBool("twitter-force")
	twitterDryRun, _ := cmd.Flags().GetBool("twitter-dry-run")

	// Parse plugin selection
	selectedPlugins := make(map[string]bool)
	for _, p := range strings.Split(pluginsStr, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			selectedPlugins[p] = true
		}
	}
	runNews := selectedPlugins["news"]
	runTwitter := selectedPlugins["twitter"]

	if !runNews && !runTwitter {
		return fmt.Errorf("no valid plugins selected (available: news, twitter)")
	}

	// Validate flags
	if newsCacheOnly && newsCacheDir == "" {
		return fmt.Errorf("--news-cache-only requires --news-cache-dir to be set")
	}
	if sinceStr != "" && days > 0 {
		return fmt.Errorf("--since and --days are mutually exclusive, use only one")
	}
	if windowDays > 0 && sinceStr == "" && days == 0 {
		return fmt.Errorf("--window-days requires --since or --days to be set")
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
	queue, err := storer.GetSearchQueue(ctx)
	if err != nil {
		return fmt.Errorf("failed to get search queue: %w", err)
	}

	if len(queue) == 0 {
		log.Info().Msg("Search queue is empty")
		fmt.Println("Search queue is empty")
		return nil
	}

	log.Info().Int("queue_size", len(queue)).Msg("Loaded search queue")

	// Apply limit
	if limit > 0 && len(queue) > limit {
		queue = queue[:limit]
		log.Info().Int("limited_to", limit).Msg("Applied limit to queue")
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

	// Initialize news-specific components if needed
	var serpAPI *search.SerpAPINewsProvider
	var rawExtractor extractor.ContentExtractor
	if runNews {
		serpAPI, err = search.NewSerpAPINewsProviderFromEnv()
		if err != nil {
			return fmt.Errorf("failed to initialize SerpAPI: %w", err)
		}

		if !newsCacheOnly {
			rawExtractor, err = extractor.NewExtractorFromType(newsExtractorType)
			if err != nil {
				return fmt.Errorf("failed to initialize content extractor: %w", err)
			}
		}
	}

	// Initialize twitter-specific components if needed
	var twitterFetcher twitter.TweetFetcher
	if runTwitter {
		twitterFetcher, err = twitter.NewTwitterAPIIOFetcherFromEnv()
		if err != nil {
			log.Warn().Err(err).Msg("Failed to initialize twitter fetcher, skipping twitter plugin")
			runTwitter = false
		}
	}

	pluginNames := make([]string, 0)
	if runNews {
		pluginNames = append(pluginNames, "news")
	}
	if runTwitter {
		pluginNames = append(pluginNames, "twitter")
	}

	log.Info().
		Int("queue_size", len(queue)).
		Strs("plugins", pluginNames).
		Msg("Starting queue processing")

	// Process each person in the queue
	totalNewsFacts := 0
	totalTwitterFacts := 0
	successCount := 0
	errorCount := 0

	for i, entry := range queue {
		log.Info().
			Int("progress", i+1).
			Int("total", len(queue)).
			Str("person", entry.Name).
			Str("type", entry.EntityType).
			Msg("Processing queue entry")

		personSuccess := false

		// --- News plugin ---
		if runNews {
			targetType := news.TargetPerson
			if entry.EntityType == "party" {
				targetType = news.TargetParty
			}
			target := news.SearchTarget{
				Name:     entry.Name,
				Type:     targetType,
				PersonID: &entry.PersonID,
			}

			// Create content extractor with target-specific caching
			var contentExt extractor.ContentExtractor
			if newsCacheOnly {
				targetCacheDir := queueTargetCacheDir(newsCacheDir, target.Name)
				contentExt = extractor.NewCacheOnlyExtractor(targetCacheDir)
			} else if newsCacheDir != "" {
				targetCacheDir := queueTargetCacheDir(newsCacheDir, target.Name)
				contentExt = extractor.NewCacheExtractor(rawExtractor, targetCacheDir, true, true)
			} else {
				contentExt = rawExtractor
			}

			batchExtractor := core.NewBatchExtractorFromEnv(contentExt)

			newsProvider := &news.Provider{
				NewsSearch:       serpAPI,
				ContentExtractor: batchExtractor,
				FactExtractor:    factExtractor,
				Embedder:         embedder,
				Storer:           storer,
			}

			newsOpts := news.Options{
				DateFrom:     since,
				MaxParse:     newsMaxParse,
				Location:     newsLocation,
				RevisitHours: 24 * 365 * 10,
				MaxPerStory:  newsMaxPerStory,
			}

			var newsResult *news.Result
			if windowDays > 0 {
				newsOpts.WindowDays = windowDays
				newsResult, err = newsProvider.CollectFactsWithDateWindows(ctx, target, newsOpts)
			} else {
				newsResult, err = newsProvider.CollectFacts(ctx, target, newsOpts)
			}
			if err != nil {
				log.Error().Err(err).Str("person", entry.Name).Msg("News plugin failed")
			} else {
				totalNewsFacts += newsResult.FactsExtracted
				personSuccess = true
				log.Info().
					Str("person", entry.Name).
					Int("articles_found", newsResult.ArticlesFound).
					Int("articles_parsed", newsResult.ArticlesParsed).
					Int("facts_extracted", newsResult.FactsExtracted).
					Msg("News plugin completed")
			}
		}

		// --- Twitter plugin ---
		if runTwitter {
			handles, err := storer.GetTwitterHandlesByPersonID(ctx, entry.PersonID)
			if err != nil {
				log.Warn().Err(err).Str("person", entry.Name).Msg("Failed to get twitter handles")
			} else if len(handles) > 0 {
				twitterProvider := &twitter.Provider{
					Fetcher:       twitterFetcher,
					FactExtractor: factExtractor,
					Embedder:      embedder,
					Storer:        storer,
				}

				twitterOpts := twitter.Options{
					MaxTweets:  twitterMaxTweets,
					BatchSize:  twitterBatchSize,
					Force:      twitterForce,
					DryRun:     twitterDryRun,
					WindowDays: windowDays,
					DateFrom:   since,
				}

				for _, handle := range handles {
					var twitterResult *twitter.Result
					if windowDays > 0 {
						twitterResult, err = twitterProvider.CollectFactsWithDateWindows(ctx, handle, twitterOpts)
					} else {
						twitterResult, err = twitterProvider.CollectFacts(ctx, handle, twitterOpts)
					}
					if err != nil {
						log.Error().Err(err).Str("handle", handle.Handle).Msg("Twitter plugin failed for handle")
					} else {
						totalTwitterFacts += twitterResult.FactsExtracted
						personSuccess = true
						log.Info().
							Str("handle", handle.Handle).
							Int("tweets_fetched", twitterResult.TweetsFetched).
							Int("facts_extracted", twitterResult.FactsExtracted).
							Msg("Twitter plugin completed for handle")
					}
				}
			} else {
				log.Debug().Str("person", entry.Name).Msg("No twitter handles found, skipping twitter plugin")
			}
		}

		if personSuccess {
			successCount++
		} else {
			errorCount++
		}

		fmt.Printf("[%d/%d] %s: news=%d facts, twitter=%d facts\n",
			i+1, len(queue), entry.Name, totalNewsFacts, totalTwitterFacts)
	}

	log.Info().
		Int("total_processed", successCount).
		Int("total_errors", errorCount).
		Int("news_facts", totalNewsFacts).
		Int("twitter_facts", totalTwitterFacts).
		Msg("Queue processing completed")

	fmt.Printf("\nProcessing complete:\n")
	fmt.Printf("  Plugins: %s\n", strings.Join(pluginNames, ", "))
	fmt.Printf("  Successful: %d\n", successCount)
	fmt.Printf("  Errors: %d\n", errorCount)
	fmt.Printf("  News facts: %d\n", totalNewsFacts)
	fmt.Printf("  Twitter facts: %d\n", totalTwitterFacts)

	return nil
}

// queueTargetCacheDir returns the target-specific cache subdirectory.
func queueTargetCacheDir(baseDir string, targetName string) string {
	normalizedName := util.NormalizeToDirName(targetName)
	targetDir := filepath.Join(baseDir, normalizedName)

	if err := os.MkdirAll(targetDir, 0755); err != nil {
		log.Warn().Err(err).Str("dir", targetDir).Msg("Failed to create target cache directory")
		return baseDir
	}

	return targetDir
}
