package cmd

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/plugins/news"
	"digital-mirror/backend/storage"

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
	Use:         "search",
	Short:       "Search news for specific targets",
	Long:        `Search news sources for mentions of specific persons or parties and extract facts.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runNewsSearch,
}

func init() {
	pluginCmd.AddCommand(newsCmd)
	newsCmd.AddCommand(newsSearchCmd)

	// Target selection
	newsSearchCmd.Flags().StringSlice("targets", nil, "specific persons/parties to search news for (by name)")
	newsSearchCmd.Flags().BoolP("force", "f", false, "force create missing persons/parties without prompting")

	// News search parameters
	newsSearchCmd.Flags().String("since", "", "only search news articles published after YYYY-MM-DD")
	newsSearchCmd.Flags().Int("max-parse", 5, "maximum news articles to parse and extract facts from per target")
	newsSearchCmd.Flags().String("location", "France", "restrict news search to this country")
	newsSearchCmd.Flags().Bool("skip-unknown", false, "skip targets with no prior news search results")
	newsSearchCmd.Flags().Bool("dry-run", false, "show what URLs would be processed without actually fetching or storing")
	newsSearchCmd.Flags().String("extractor", "auto", "content extractor to use (auto, local, webcrawler, firecrawl, apify)")
	newsSearchCmd.Flags().String("cache-dir", "", "directory to cache extracted content (enables caching)")
	newsSearchCmd.Flags().Bool("cache-only", false, "only load from cache, don't scrape (requires --cache-dir)")
	newsSearchCmd.Flags().Int("window-days", 0, "break date range into windows of N days (requires --since, 0=disabled)")
	newsSearchCmd.Flags().Int("max-per-story", 0, "max articles per story_token (0=no cap)")
	newsSearchCmd.Flags().String("keep-source", "", "dump article content + extracted facts to this directory for debugging")
}

func runNewsSearch(cmd *cobra.Command, args []string) error {
	// Load environment variables
	err := godotenv.Load()
	if err != nil {
		log.Warn().Msg("No .env file found, relying on environment variables")
	}

	ctx := context.Background()

	// Initialize database connection
	storer, err := deps.OpenStorer(ctx)
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
	windowDays, _ := cmd.Flags().GetInt("window-days")
	maxPerStory, _ := cmd.Flags().GetInt("max-per-story")
	keepSourceDir, _ := cmd.Flags().GetString("keep-source")

	// Validate cache-only requires cache-dir
	if cacheOnly && cacheDir == "" {
		return fmt.Errorf("--cache-only requires --cache-dir to be set")
	}

	// Validate window-days requires --since
	if windowDays > 0 && sinceStr == "" {
		return fmt.Errorf("--window-days requires --since to be set")
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
	var searchTargets []plugins.Target

	if len(targets) > 0 {
		log.Info().Int("count", len(targets)).Msg("Resolving specified news targets")
		for _, query := range targets {
			person, ok, err := resolveNewsTarget(ctx, storer, query, force)
			if err != nil {
				log.Error().Err(err).Str("query", query).Msg("Failed to resolve target")
				continue
			}
			if !ok {
				continue
			}
			searchTargets = append(searchTargets, plugins.Target{PersonID: person.ID, Name: person.Name, EntityType: person.EntityType})
		}
		if len(searchTargets) == 0 {
			return fmt.Errorf("no targets resolved")
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
			searchTargets = append(searchTargets, plugins.Target{
				PersonID:   p.ID,
				Name:       p.Name,
				EntityType: p.EntityType,
			})
		}

		log.Info().Int("count", len(searchTargets)).Msg("Loaded persons from database")
	}

	if len(searchTargets) == 0 {
		log.Info().Msg("No targets for news search")
		return nil
	}

	provider, err := news.NewProviderFromDeps(deps.New(storer))
	if err != nil {
		return fmt.Errorf("failed to initialize news provider: %w", err)
	}
	if extractorType != "auto" {
		if err := provider.UseExtractor(extractorType); err != nil {
			return fmt.Errorf("failed to initialize content extractor: %w", err)
		}
	}

	// Set up search options
	opts := news.Options{
		DateFrom:      since,
		MaxParse:      maxParse,
		Location:      location,
		RevisitHours:  24 * 365 * 10, // Never re-fetch same URL (10 years)
		DryRun:        dryRun,
		WindowDays:    windowDays,
		MaxPerStory:   maxPerStory,
		KeepSourceDir: keepSourceDir,
		CacheDir:      cacheDir,
		CacheOnly:     cacheOnly,
	}

	// Prioritize targets
	prioritizeOpts := news.PrioritizationOptions{
		PartiesFirst:  true,
		SkipNoResults: skipUnknown,
		LookbackDays:  30,
	}

	prioritizedTargets, err := provider.PrioritizeTargets(ctx, searchTargets, prioritizeOpts)
	if err != nil {
		return fmt.Errorf("failed to prioritize targets: %w", err)
	}

	log.Info().Int("targets", len(prioritizedTargets)).Msg("Prioritized targets for news search")

	results, err := provider.CollectFactsForTargets(ctx, prioritizedTargets, opts)
	if err != nil {
		return fmt.Errorf("news search: %w", err)
	}

	totalFacts := 0
	totalSkippedDedup := 0
	totalSkipped := 0
	for _, result := range results {
		totalFacts += result.FactsExtracted
		totalSkippedDedup += result.FactsSkippedDedup
		totalSkipped += result.SkippedAlreadyProcessed
	}

	totalRaw := totalFacts + totalSkippedDedup
	dedupRate := 0.0
	if totalRaw > 0 {
		dedupRate = float64(totalSkippedDedup) / float64(totalRaw) * 100
	}
	log.Info().
		Int("total_targets", len(prioritizedTargets)).
		Int("total_facts_stored", totalFacts).
		Int("total_facts_skipped_dedup", totalSkippedDedup).
		Float64("dedup_rate_pct", dedupRate).
		Int("total_skipped_already_processed", totalSkipped).
		Msg("News search completed successfully")

	metrics.IncrementFactsSkippedDedup(totalSkippedDedup)

	// Record job metrics (skip in dry-run and cache-only modes)
	if !dryRun && !cacheOnly {
		var metrics []storage.JobMetric
		for _, r := range results {
			metrics = append(metrics, jobMetrics("news-search", r.Target.Name, map[string]float64{
				"articles_found":            float64(r.ArticlesFound),
				"articles_parsed":           float64(r.ArticlesParsed),
				"facts_extracted":           float64(r.FactsExtracted),
				"facts_skipped_dedup":       float64(r.FactsSkippedDedup),
				"skipped_already_processed": float64(r.SkippedAlreadyProcessed),
				"errors":                    float64(len(r.Errors)),
			})...)
		}
		recordJobMetrics(ctx, storer, metrics)
	}

	return nil
}

// resolveNewsTarget resolves one --targets entry. With --force a confident
// fuzzy match is taken and an unknown name is created as a human without
// asking; otherwise the user disambiguates or confirms the creation.
func resolveNewsTarget(ctx context.Context, storer *storage.PostgresStorer, query string, force bool) (core.PersonRecord, bool, error) {
	if !force {
		return resolvePersonInteractive(ctx, storer, query, true)
	}
	person, err := resolveOrCreatePerson(ctx, storer, query, "human")
	if err != nil {
		return core.PersonRecord{}, false, err
	}
	return person, true, nil
}
