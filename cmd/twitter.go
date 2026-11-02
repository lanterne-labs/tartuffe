package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/plugins/twitter"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// twitterCmd is the parent command for twitter subcommands
var twitterCmd = &cobra.Command{
	Use:   "twitter",
	Short: "Manage Twitter/X handles and fetch tweets",
	Long:  `Link Twitter handles to persons, list them, and fetch tweets for fact extraction.`,
}

// twitterAddCmd adds a Twitter handle to a person
var twitterAddCmd = &cobra.Command{
	Use:         "add",
	Short:       "Link a Twitter handle to a person",
	Long:        `Link a Twitter/X handle to a person in the database.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runTwitterAdd,
}

// twitterRemoveCmd removes a Twitter handle from a person
var twitterRemoveCmd = &cobra.Command{
	Use:         "remove",
	Short:       "Remove a Twitter handle from a person",
	Long:        `Remove a Twitter/X handle link from a person.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runTwitterRemove,
}

// twitterListCmd lists all active Twitter handles
var twitterListCmd = &cobra.Command{
	Use:         "list",
	Short:       "List all active Twitter handles",
	Long:        `List all Twitter/X handles linked to persons.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runTwitterList,
}

// twitterFetchCmd fetches tweets and extracts facts
var twitterFetchCmd = &cobra.Command{
	Use:         "fetch",
	Short:       "Fetch tweets and extract facts",
	Long:        `Fetch tweets from linked Twitter/X handles and extract facts using LLM.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runTwitterFetch,
}

// twitterDiscoverCmd auto-discovers Twitter handles for persons
var twitterDiscoverCmd = &cobra.Command{
	Use:         "discover",
	Short:       "Auto-discover Twitter handles for persons",
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runTwitterDiscover,
}

// twitterFactsCmd lists facts extracted from twitter
var twitterFactsCmd = &cobra.Command{
	Use:         "facts",
	Short:       "List facts extracted from Twitter",
	Long:        `List all facts that were extracted from Twitter/X, grouped by person.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runTwitterFacts,
}

var twitterRedateCmd = &cobra.Command{
	Use:   "redate",
	Short: "Move every tweet fact to the tweet that holds its quote (URL and date)",
	Long: `Until 2026-10-30 the facts of a batch of tweets all carried the profile URL
and the date of the batch's first tweet. This fetches again, by id, the text of
the tweets deleted at extraction (twitterapi.io, TWITTER_API_IO_KEY), stores it,
and matches each tweet fact to its tweet by its quote. Without --apply it only
reports; --output writes the facts no tweet holds as JSONL.`,
	RunE: runTwitterRedate,
}

func init() {
	twitterCmd.AddCommand(twitterRedateCmd)
	twitterRedateCmd.Flags().Bool("apply", false, "write the new URL and date of the matched facts")
	twitterRedateCmd.Flags().String("output", "", "JSONL file of the facts no tweet holds")

	pluginCmd.AddCommand(twitterCmd)
	twitterCmd.AddCommand(twitterAddCmd)
	twitterCmd.AddCommand(twitterRemoveCmd)
	twitterCmd.AddCommand(twitterListCmd)
	twitterCmd.AddCommand(twitterFetchCmd)
	twitterCmd.AddCommand(twitterFactsCmd)
	twitterCmd.AddCommand(twitterDiscoverCmd)

	// discover flags
	twitterDiscoverCmd.Flags().Bool("dry-run", false, "show discovered handles without saving")
	twitterDiscoverCmd.Flags().String("person", "", "discover handle for a specific person only")
	twitterDiscoverCmd.Flags().Bool("all", true, "discover handles for all persons without handles (default)")
	twitterDiscoverCmd.Flags().Bool("force", false, "re-search even persons searched recently or who already have handles")

	// add flags
	twitterAddCmd.Flags().String("person", "", "person name to link the handle to (required)")
	twitterAddCmd.Flags().String("handle", "", "Twitter/X handle without @ (required)")
	twitterAddCmd.MarkFlagRequired("person")
	twitterAddCmd.MarkFlagRequired("handle")

	// remove flags
	twitterRemoveCmd.Flags().String("person", "", "person name (required)")
	twitterRemoveCmd.Flags().String("handle", "", "Twitter/X handle to remove (required)")
	twitterRemoveCmd.MarkFlagRequired("person")
	twitterRemoveCmd.MarkFlagRequired("handle")

	// fetch flags
	twitterFetchCmd.Flags().Int64("person-id", 0, "fetch tweets only for this person ID")
	twitterFetchCmd.Flags().Int("max-tweets", 50, "maximum tweets to fetch per handle")
	twitterFetchCmd.Flags().Int("batch-size", 20, "tweets per LLM extraction call")
	twitterFetchCmd.Flags().String("since", "", "fetch tweets from this date (YYYY-MM-DD)")
	twitterFetchCmd.Flags().String("until", "", "fetch tweets until this date (YYYY-MM-DD)")
	twitterFetchCmd.Flags().Int("window-days", 0, "break date range into windows of N days (requires --since)")
	twitterFetchCmd.Flags().Bool("force", false, "bypass dedup, re-fetch and re-process everything")
	twitterFetchCmd.Flags().Bool("dry-run", false, "show what would be done without fetching or storing")
}

func runTwitterAdd(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	personName, _ := cmd.Flags().GetString("person")
	handle, _ := cmd.Flags().GetString("handle")

	person, err := core.ResolvePersonID(ctx, storer, personName, fuzzyFloor)
	if err != nil {
		return err
	}
	personID := person.ID
	log.Info().Str("person", person.Name).Int64("id", personID).Msg("Found person")

	if err := twitter.NewStore(storer).AddHandle(ctx, personID, handle); err != nil {
		return fmt.Errorf("failed to add twitter handle: %w", err)
	}

	fmt.Printf("Linked @%s to person ID %d\n", handle, personID)
	return nil
}

func runTwitterRemove(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	personName, _ := cmd.Flags().GetString("person")
	handle, _ := cmd.Flags().GetString("handle")

	person, err := core.ResolvePersonID(ctx, storer, personName, core.ExactMatchOnly)
	if err != nil {
		return err
	}

	personID := person.ID
	if err := twitter.NewStore(storer).RemoveHandle(ctx, personID, handle); err != nil {
		return fmt.Errorf("failed to remove twitter handle: %w", err)
	}

	fmt.Printf("Removed @%s from person ID %d\n", handle, personID)
	return nil
}

func runTwitterList(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	handles, err := twitter.NewStore(storer).GetActiveHandles(ctx)
	if err != nil {
		return fmt.Errorf("failed to list twitter handles: %w", err)
	}

	if len(handles) == 0 {
		fmt.Println("No active Twitter handles.")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "PERSON_ID\tPERSON\tHANDLE\tLAST_FETCHED")
	for _, h := range handles {
		lastFetched := "never"
		if h.LastFetchedAt != nil {
			lastFetched = h.LastFetchedAt.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%d\t%s\t@%s\t%s\n", h.PersonID, h.PersonName, h.Handle, lastFetched)
	}
	w.Flush()

	return nil
}

func runTwitterFetch(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	// Parse flags
	personID, _ := cmd.Flags().GetInt64("person-id")
	maxTweets, _ := cmd.Flags().GetInt("max-tweets")
	batchSize, _ := cmd.Flags().GetInt("batch-size")
	sinceStr, _ := cmd.Flags().GetString("since")
	untilStr, _ := cmd.Flags().GetString("until")
	windowDays, _ := cmd.Flags().GetInt("window-days")
	force, _ := cmd.Flags().GetBool("force")
	dryRun, _ := cmd.Flags().GetBool("dry-run")

	// Validate window-days requires --since
	if windowDays > 0 && sinceStr == "" {
		return fmt.Errorf("--window-days requires --since to be set")
	}

	// Parse dates
	var since, until *time.Time
	if sinceStr != "" {
		t, err := time.Parse("2006-01-02", sinceStr)
		if err != nil {
			return fmt.Errorf("invalid --since format, expected YYYY-MM-DD: %w", err)
		}
		since = &t
	}
	if untilStr != "" {
		t, err := time.Parse("2006-01-02", untilStr)
		if err != nil {
			return fmt.Errorf("invalid --until format, expected YYYY-MM-DD: %w", err)
		}
		until = &t
	}

	// Get handles to process
	store := twitter.NewStore(storer)
	var handles []twitter.Handle
	if personID > 0 {
		handles, err = store.GetHandlesByPersonID(ctx, personID)
		if err != nil {
			return fmt.Errorf("failed to get twitter handles for person %d: %w", personID, err)
		}
		if len(handles) == 0 {
			return fmt.Errorf("no twitter handles found for person ID %d", personID)
		}
	} else {
		handles, err = store.GetActiveHandles(ctx)
		if err != nil {
			return fmt.Errorf("failed to get active twitter handles: %w", err)
		}
		if len(handles) == 0 {
			log.Info().Msg("No active twitter handles to fetch")
			return nil
		}
	}

	// A dry run only fetches and prints tweets, so it does not need the LLM
	// components a full run gets from the shared container.
	var provider *twitter.Provider
	if dryRun {
		fetcher, err := twitter.NewTwitterAPIIOFetcherFromEnv()
		if err != nil {
			return fmt.Errorf("failed to initialize twitter fetcher: %w", err)
		}
		provider = &twitter.Provider{Fetcher: fetcher, Storer: storer, Store: store}
	} else {
		provider, err = twitter.NewProviderFromDeps(deps.New(storer))
		if err != nil {
			return fmt.Errorf("failed to initialize twitter provider: %w", err)
		}
	}

	opts := twitter.Options{
		MaxTweets:  maxTweets,
		BatchSize:  batchSize,
		Force:      force,
		DryRun:     dryRun,
		WindowDays: windowDays,
		DateFrom:   since,
		DateTo:     until,
	}

	log.Info().
		Int("handles", len(handles)).
		Int("max_tweets", maxTweets).
		Bool("force", force).
		Bool("dry_run", dryRun).
		Msg("Starting twitter fetch")

	type handleResult struct {
		handle          string
		tweetsFetched   int
		tweetsProcessed int
		factsExtracted  int
		skippedRetweets int
		errorCount      int
	}
	var allHandleResults []handleResult

	totalFacts := 0
	totalTweets := 0

	for i, handle := range handles {
		log.Info().
			Int("progress", i+1).
			Int("total", len(handles)).
			Str("handle", handle.Handle).
			Str("person", handle.PersonName).
			Msg("Processing twitter handle")

		var result *twitter.Result
		if windowDays > 0 {
			result, err = provider.CollectFactsWithDateWindows(ctx, handle, opts)
		} else {
			result, err = provider.CollectFacts(ctx, handle, opts)
		}

		if err != nil {
			log.Error().Err(err).Str("handle", handle.Handle).Msg("Failed to process twitter handle")
			continue
		}

		log.Info().
			Str("handle", handle.Handle).
			Int("tweets_fetched", result.TweetsFetched).
			Int("tweets_processed", result.TweetsProcessed).
			Int("facts_extracted", result.FactsExtracted).
			Int("skipped_retweets", result.SkippedRetweets).
			Int("skipped_already_processed", result.SkippedAlreadyProcessed).
			Bool("skipped_range", result.SkippedAlreadyFetchedRange).
			Msg("Completed twitter handle")

		allHandleResults = append(allHandleResults, handleResult{
			handle:          handle.Handle,
			tweetsFetched:   result.TweetsFetched,
			tweetsProcessed: result.TweetsProcessed,
			factsExtracted:  result.FactsExtracted,
			skippedRetweets: result.SkippedRetweets,
			errorCount:      len(result.Errors),
		})

		totalFacts += result.FactsExtracted
		totalTweets += result.TweetsFetched
	}

	log.Info().
		Int("total_handles", len(handles)).
		Int("total_tweets", totalTweets).
		Int("total_facts", totalFacts).
		Msg("Twitter fetch completed")

	// Record job metrics (skip in dry-run mode)
	if !dryRun {
		var metrics []storage.JobMetric
		for _, hr := range allHandleResults {
			metrics = append(metrics, jobMetrics("twitter-fetch", hr.handle, map[string]float64{
				"tweets_fetched":   float64(hr.tweetsFetched),
				"tweets_processed": float64(hr.tweetsProcessed),
				"facts_extracted":  float64(hr.factsExtracted),
				"skipped_retweets": float64(hr.skippedRetweets),
				"errors":           float64(hr.errorCount),
			})...)
		}
		recordJobMetrics(ctx, storer, metrics)
	}

	return nil
}

func runTwitterDiscover(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	personFilter, _ := cmd.Flags().GetString("person")
	force, _ := cmd.Flags().GetBool("force")

	_, err = twitter.Discover(ctx, deps.New(storer), twitter.DiscoveryOptions{
		DryRun:       dryRun,
		PersonFilter: personFilter,
		Force:        force,
	})
	return err
}

func runTwitterFacts(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	factsByPerson, err := storer.GetFactsBySourceType(ctx, "twitter")
	if err != nil {
		return fmt.Errorf("failed to retrieve twitter facts: %w", err)
	}

	if len(factsByPerson) == 0 {
		fmt.Println("No facts extracted from Twitter yet.")
		return nil
	}

	totalFacts := 0
	for personName, facts := range factsByPerson {
		fmt.Printf("\n## %s (%d facts)\n", personName, len(facts))
		for _, f := range facts {
			fmt.Printf("  [%d] %s\n", f.FactID, f.FactText)
			fmt.Printf("       source: %s\n", f.SourceURL)
			if f.OriginalQuote != "" {
				fmt.Printf("       quote:  %s\n", f.OriginalQuote)
			}
		}
		totalFacts += len(facts)
	}

	fmt.Printf("\nTotal: %d facts from Twitter across %d persons\n", totalFacts, len(factsByPerson))
	return nil
}

func runTwitterRedate(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()
	fetcher, err := twitter.NewTwitterAPIIOFetcherFromEnv()
	if err != nil {
		return err
	}
	apply, _ := cmd.Flags().GetBool("apply")
	output, _ := cmd.Flags().GetString("output")

	rep, err := twitter.Redate(ctx, storer, fetcher, twitter.RedateOptions{Apply: apply})
	if err != nil {
		return err
	}
	fmt.Printf("tweets: %d read, %d fetched again, %d no longer available\n", rep.Tweets, rep.TweetsFetched, rep.TweetsMissing)
	fmt.Printf("fact sources: %d read, %d matched to their tweet (%d with another date), %d unmatched\n",
		rep.Sources, rep.Matched, rep.DateChanged, len(rep.Unmatched))
	if !apply {
		fmt.Println("dry run: nothing written to fact_sources (use --apply)")
	}
	if output != "" {
		f, err := os.Create(output)
		if err != nil {
			return fmt.Errorf("creating %s: %w", output, err)
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		for _, u := range rep.Unmatched {
			if err := enc.Encode(u); err != nil {
				return fmt.Errorf("writing %s: %w", output, err)
			}
		}
		fmt.Printf("unmatched facts written to %s\n", output)
	}
	return nil
}
