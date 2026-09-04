package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"digital-mirror/backend/plugins/frpolitics"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// pluginCmd represents the plugin command
var pluginCmd = &cobra.Command{
	Use:   "plugin",
	Short: "Commands for running plugins",
	Long:  `Plugin commands for running various data processing plugins.`,
}

// frpoliticsCmd represents the frpolitics plugin command
var frpoliticsCmd = &cobra.Command{
	Use:   "frpolitics",
	Short: "Run the French politics plugin",
	Long:  `Runs the French politics plugin to process Assemblée Nationale data.`,
	RunE:  runFrpoliticsPlugin,
}

func init() {
	rootCmd.AddCommand(pluginCmd)
	pluginCmd.AddCommand(frpoliticsCmd)

	// Flags for frpolitics
	frpoliticsCmd.Flags().String("mode", "votes", "operation mode: 'votes' (process official votes), 'news' (search news only), or 'both' (votes then news)")
	frpoliticsCmd.Flags().String("since", "", "process laws voted on or after YYYY-MM-DD (auto-derives legislatures)")
	frpoliticsCmd.Flags().Int("last-years", 0, "process votes from the last N years, e.g. --last-years 2 (auto-derives since date and legislatures)")
	frpoliticsCmd.Flags().Int("from-year", 0, "process votes starting from Jan 1 of this year, e.g. --from-year 2022 (auto-derives legislatures)")
	frpoliticsCmd.Flags().Int("max-laws", 10, "maximum laws to process")
	frpoliticsCmd.Flags().Float64("party-threshold", 0.8, "majority threshold for party-level facts (0..1)")
	frpoliticsCmd.Flags().String("legislatures", "", "comma-separated legislature numbers to fetch (default: auto-detect from date range or current)")

	// News search flags
	frpoliticsCmd.Flags().String("news-since", "", "only search news articles published after YYYY-MM-DD")
	frpoliticsCmd.Flags().Int("news-limit", 5, "maximum news articles to process per target")
	frpoliticsCmd.Flags().String("news-location", "France", "restrict news search to this country")
	frpoliticsCmd.Flags().Bool("news-skip-unknown", false, "skip targets with no prior news search results")
	frpoliticsCmd.Flags().StringSlice("news-targets", nil, "specific persons/parties to search news for (by name)")
	frpoliticsCmd.Flags().String("extractor", "webcrawler", "content extractor to use (webcrawler, firecrawl, apify)")
	frpoliticsCmd.Flags().String("cache-dir", "", "directory to cache extracted content (enables caching)")
	frpoliticsCmd.Flags().Bool("cache-only", false, "only load from cache, don't scrape (requires --cache-dir)")
}

func runFrpoliticsPlugin(cmd *cobra.Command, args []string) error {
	// Load environment variables from .env file
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
	mode, _ := cmd.Flags().GetString("mode")
	sinceStr, _ := cmd.Flags().GetString("since")
	lastYears, _ := cmd.Flags().GetInt("last-years")
	fromYear, _ := cmd.Flags().GetInt("from-year")
	maxLaws, _ := cmd.Flags().GetInt("max-laws")
	threshold, _ := cmd.Flags().GetFloat64("party-threshold")
	legsStr, _ := cmd.Flags().GetString("legislatures")

	// News search flags
	newsSinceStr, _ := cmd.Flags().GetString("news-since")
	newsLimit, _ := cmd.Flags().GetInt("news-limit")
	newsLocation, _ := cmd.Flags().GetString("news-location")
	newsSkipUnknown, _ := cmd.Flags().GetBool("news-skip-unknown")
	newsTargets, _ := cmd.Flags().GetStringSlice("news-targets")
	extractorType, _ := cmd.Flags().GetString("extractor")
	cacheDir, _ := cmd.Flags().GetString("cache-dir")
	cacheOnly, _ := cmd.Flags().GetBool("cache-only")

	// Validate cache-only requires cache-dir
	if cacheOnly && cacheDir == "" {
		return fmt.Errorf("--cache-only requires --cache-dir to be set")
	}

	// Validate mode
	if mode != "votes" && mode != "news" && mode != "both" {
		return fmt.Errorf("invalid mode %q: must be 'votes', 'news', or 'both'", mode)
	}

	// Parse since date (--since takes priority; --last-years and --from-year are convenience shorthands)
	var since *time.Time
	if sinceStr != "" {
		if t, err := time.Parse("2006-01-02", sinceStr); err == nil {
			since = &t
		} else {
			log.Warn().Str("since", sinceStr).Msg("invalid --since format, expected YYYY-MM-DD")
		}
	} else if lastYears > 0 {
		t := time.Now().AddDate(-lastYears, 0, 0)
		since = &t
		log.Info().Int("last_years", lastYears).Str("since", t.Format("2006-01-02")).Msg("derived --since from --last-years")
	} else if fromYear > 0 {
		t := time.Date(fromYear, time.January, 1, 0, 0, 0, 0, time.UTC)
		since = &t
		log.Info().Int("from_year", fromYear).Str("since", t.Format("2006-01-02")).Msg("derived --since from --from-year")
	}

	// Parse news-since date
	var newsSince *time.Time
	if newsSinceStr != "" {
		if t, err := time.Parse("2006-01-02", newsSinceStr); err == nil {
			newsSince = &t
		} else {
			log.Warn().Str("news-since", newsSinceStr).Msg("invalid --news-since format, expected YYYY-MM-DD")
		}
	}

	// Parse legislatures
	var legs []int
	if legsStr != "" {
		for _, p := range strings.Split(legsStr, ",") {
			p = strings.TrimSpace(p)
			if p == "" {
				continue
			}
			if n, err := strconv.Atoi(p); err == nil {
				legs = append(legs, n)
			} else {
				log.Warn().Str("leg", p).Msg("invalid legislature")
			}
		}
	}

	// Set up options
	opts := frpolitics.Options{
		Mode:           mode,
		Since:          since,
		MaxLaws:        maxLaws,
		PartyThreshold: threshold,
		Legislatures:   legs,
		RevisitHours:   -1, // negative = always visitable: retry laws whose store failed (0 would coerce to the 24h default)

		// News search options
		NewsSince:        newsSince,
		NewsLimit:        newsLimit,
		NewsLocation:     newsLocation,
		NewsSkipUnknown:  newsSkipUnknown,
		NewsTargets:      newsTargets,
		ContentExtractor: extractorType,
		CacheDir:         cacheDir,
		CacheOnly:        cacheOnly,
	}

	sinceLog := ""
	if since != nil {
		sinceLog = since.Format("2006-01-02")
	}
	log.Info().
		Str("mode", mode).
		Str("since", sinceLog).
		Int("max_laws", maxLaws).
		Float64("party_threshold", threshold).
		Ints("legislatures", legs).
		Int("news_limit", newsLimit).
		Str("news_location", newsLocation).
		Int("news_targets", len(newsTargets)).
		Msg("Starting frpolitics plugin")

	// Run the plugin
	if err := frpolitics.Run(ctx, storer, opts); err != nil {
		// Check if it's an ambiguous names error
		if ambigErr, ok := err.(*frpolitics.AmbiguousNamesError); ok {
			// Handle disambiguation
			fmt.Println("\nSome target names need disambiguation or don't exist yet:")
			fmt.Println()

			resolvedTargets := make([]string, 0)

			for query, result := range ambigErr.Resolutions {
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
						i+1, match.Target.Name, match.Target.EntityType, match.Score)
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
				return nil
			}

			// Re-run with resolved names
			fmt.Printf("\nRe-running with resolved targets: %v\n\n", resolvedTargets)
			opts.NewsTargets = resolvedTargets

			if err := frpolitics.Run(ctx, storer, opts); err != nil {
				return fmt.Errorf("frpolitics plugin failed after disambiguation: %w", err)
			}

			log.Info().Msg("Frpolitics plugin completed successfully")
			return nil
		}

		return fmt.Errorf("frpolitics plugin failed: %w", err)
	}

	log.Info().Msg("Frpolitics plugin completed successfully")
	return nil
}
