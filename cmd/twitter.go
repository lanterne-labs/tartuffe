package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"text/tabwriter"
	"time"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins/twitter"
	"digital-mirror/backend/search"
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

func init() {
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

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	personName, _ := cmd.Flags().GetString("person")
	handle, _ := cmd.Flags().GetString("handle")

	// Resolve person name using fuzzy matching
	result := core.ResolveTargetName(ctx, storer, personName)
	if result.Error != nil {
		return fmt.Errorf("failed to resolve person: %w", result.Error)
	}

	var personID int64
	if result.IsExactMatch {
		personID = *result.ExactMatch.PersonID
		log.Info().Str("person", result.ExactMatch.Name).Int64("id", personID).Msg("Found person")
	} else if len(result.FuzzyMatches) > 0 {
		// Use the top fuzzy match
		top := result.FuzzyMatches[0]
		if top.Score >= 0.8 {
			personID = *top.Target.PersonID
			log.Info().
				Str("query", personName).
				Str("matched", top.Target.Name).
				Float64("score", top.Score).
				Msg("Using fuzzy match")
		} else {
			return fmt.Errorf("no confident match for %q (best: %s at %.2f). Use exact name or create the person first", personName, top.Target.Name, top.Score)
		}
	} else {
		return fmt.Errorf("person %q not found in database. Add them first with: dm persons add %q", personName, personName)
	}

	if err := storer.AddTwitterHandle(ctx, personID, handle); err != nil {
		return fmt.Errorf("failed to add twitter handle: %w", err)
	}

	fmt.Printf("Linked @%s to person ID %d\n", handle, personID)
	return nil
}

func runTwitterRemove(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	personName, _ := cmd.Flags().GetString("person")
	handle, _ := cmd.Flags().GetString("handle")

	// Resolve person
	result := core.ResolveTargetName(ctx, storer, personName)
	if result.Error != nil {
		return fmt.Errorf("failed to resolve person: %w", result.Error)
	}
	if !result.IsExactMatch {
		return fmt.Errorf("person %q not found in database", personName)
	}

	personID := *result.ExactMatch.PersonID
	if err := storer.RemoveTwitterHandle(ctx, personID, handle); err != nil {
		return fmt.Errorf("failed to remove twitter handle: %w", err)
	}

	fmt.Printf("Removed @%s from person ID %d\n", handle, personID)
	return nil
}

func runTwitterList(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	handles, err := storer.GetActiveTwitterHandles(ctx)
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

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
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
	var handles []storage.TwitterHandle
	if personID > 0 {
		handles, err = storer.GetTwitterHandlesByPersonID(ctx, personID)
		if err != nil {
			return fmt.Errorf("failed to get twitter handles for person %d: %w", personID, err)
		}
		if len(handles) == 0 {
			return fmt.Errorf("no twitter handles found for person ID %d", personID)
		}
	} else {
		handles, err = storer.GetActiveTwitterHandles(ctx)
		if err != nil {
			return fmt.Errorf("failed to get active twitter handles: %w", err)
		}
		if len(handles) == 0 {
			log.Info().Msg("No active twitter handles to fetch")
			return nil
		}
	}

	// Initialize fetcher
	fetcher, err := twitter.NewTwitterAPIIOFetcherFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize twitter fetcher: %w", err)
	}

	// Initialize LLM components (not needed in dry-run)
	var factExtractor *categorizer.OpenAIExtractor
	var embedder *embedding.OpenAIEmbeddingGenerator

	if !dryRun {
		factExtractor, err = categorizer.NewOpenAIExtractorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to initialize fact extractor: %w", err)
		}

		embedder, err = embedding.NewOpenAIEmbeddingGeneratorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to initialize embedder: %w", err)
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

	provider := &twitter.Provider{
		Fetcher:       fetcher,
		FactExtractor: factExtractor,
		Embedder:      embedder,
		Storer:        storer,
	}

	type handleResult struct {
		handle           string
		tweetsFetched    int
		tweetsProcessed  int
		factsExtracted   int
		skippedRetweets  int
		errorCount       int
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
		var jobMetrics []storage.JobMetric
		for _, hr := range allHandleResults {
			jobMetrics = append(jobMetrics,
				storage.JobMetric{JobType: "twitter-fetch", Target: hr.handle, Metric: "tweets_fetched", Value: float64(hr.tweetsFetched)},
				storage.JobMetric{JobType: "twitter-fetch", Target: hr.handle, Metric: "tweets_processed", Value: float64(hr.tweetsProcessed)},
				storage.JobMetric{JobType: "twitter-fetch", Target: hr.handle, Metric: "facts_extracted", Value: float64(hr.factsExtracted)},
				storage.JobMetric{JobType: "twitter-fetch", Target: hr.handle, Metric: "skipped_retweets", Value: float64(hr.skippedRetweets)},
				storage.JobMetric{JobType: "twitter-fetch", Target: hr.handle, Metric: "errors", Value: float64(hr.errorCount)},
			)
		}
		if err := storer.RecordJobMetrics(ctx, jobMetrics); err != nil {
			log.Error().Err(err).Msg("Failed to record job metrics")
		}
	}

	return nil
}

func extractTwitterHandlesFromWeb(results []search.SearchResult) []string {
	seen := make(map[string]bool)
	var handles []string

	// Pattern for x.com/username or twitter.com/username URLs
	urlRe := regexp.MustCompile(`(?:x\.com|twitter\.com)/([A-Za-z0-9_]{1,15})(?:[/?#]|$)`)
	// Pattern for @handle mentions
	atRe := regexp.MustCompile(`@([A-Za-z0-9_]{1,15})\b`)

	// Common paths to exclude
	excludePaths := map[string]bool{
		"search": true, "i": true, "hashtag": true, "intent": true,
		"home": true, "explore": true, "settings": true, "status": true,
	}

	for _, r := range results {
		// Check URLs
		for _, text := range []string{r.URL, r.Title, r.Snippet} {
			for _, match := range urlRe.FindAllStringSubmatch(text, -1) {
				handle := strings.ToLower(match[1])
				if !excludePaths[handle] && !seen[handle] {
					seen[handle] = true
					handles = append(handles, handle)
				}
			}
		}
		// Check @mentions in title and snippet
		for _, text := range []string{r.Title, r.Snippet} {
			for _, match := range atRe.FindAllStringSubmatch(text, -1) {
				handle := strings.ToLower(match[1])
				if !seen[handle] {
					seen[handle] = true
					handles = append(handles, handle)
				}
			}
		}
	}

	return handles
}

func runTwitterDiscover(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	dryRun, _ := cmd.Flags().GetBool("dry-run")
	personFilter, _ := cmd.Flags().GetString("person")

	return executeTwitterDiscovery(ctx, storer, TwitterDiscoveryOpts{
		DryRun:       dryRun,
		PersonFilter: personFilter,
	})
}

// TwitterDiscoveryOpts configures the Twitter handle auto-discovery.
type TwitterDiscoveryOpts struct {
	DryRun       bool
	PersonFilter string // if set, only discover for this person
}

// executeTwitterDiscovery searches for and links Twitter handles for persons who don't have one.
// Used by both the CLI command and the daily pipeline.
func executeTwitterDiscovery(ctx context.Context, storer *storage.PostgresStorer, opts TwitterDiscoveryOpts) error {
	// Get all person records
	allPersons, err := storer.GetAllPersonRecords(ctx)
	if err != nil {
		return fmt.Errorf("failed to get person records: %w", err)
	}

	// Filter to specific person if requested
	var persons []core.PersonRecord
	if opts.PersonFilter != "" {
		result := core.ResolveTargetName(ctx, storer, opts.PersonFilter)
		if result.Error != nil {
			return fmt.Errorf("failed to resolve person: %w", result.Error)
		}
		if result.IsExactMatch {
			for _, p := range allPersons {
				if p.ID == *result.ExactMatch.PersonID {
					persons = append(persons, p)
					break
				}
			}
		} else if len(result.FuzzyMatches) > 0 && result.FuzzyMatches[0].Score >= 0.8 {
			for _, p := range allPersons {
				if p.ID == *result.FuzzyMatches[0].Target.PersonID {
					persons = append(persons, p)
					break
				}
			}
		} else {
			return fmt.Errorf("person %q not found in database", opts.PersonFilter)
		}
	} else {
		persons = allPersons
	}

	if len(persons) == 0 {
		log.Info().Msg("No persons to process")
		return nil
	}

	// Initialize web search provider
	searcher, err := search.NewWebSearcherFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize search provider: %w", err)
	}

	// Initialize LLM client
	client := llm.NewBaseLLMClient()
	apiURL := os.Getenv("OPEN_AI_API")
	if apiURL == "" {
		apiURL = "https://api.openai.com/v1"
	}
	apiURL = apiURL + "/chat/completions"
	apiKey := os.Getenv("OPEN_AI_KEY")
	model := os.Getenv("OPEN_AI_CHAT_MODEL")
	if model == "" {
		model = "gpt-4o-mini"
	}

	var discovered int
	for i, person := range persons {
		// Check if person already has handles (skip unless --person forces it)
		existingHandles, err := storer.GetTwitterHandlesByPersonID(ctx, person.ID)
		if err != nil {
			log.Error().Err(err).Str("person", person.Name).Msg("Failed to check existing handles")
			continue
		}
		if len(existingHandles) > 0 && opts.PersonFilter == "" {
			log.Debug().Str("person", person.Name).Int("handles", len(existingHandles)).Msg("Skipping person with existing handles")
			continue
		}

		log.Info().
			Int("progress", i+1).
			Int("total", len(persons)).
			Str("person", person.Name).
			Msg("Searching for Twitter handle")

		// Search for the person's Twitter
		query := fmt.Sprintf(`"%s" twitter site:x.com`, person.Name)
		results, err := searcher.SearchWeb(query, 10)
		if err != nil {
			log.Error().Err(err).Str("person", person.Name).Msg("Search failed")
			continue
		}

		// Extract candidate handles
		candidates := extractTwitterHandlesFromWeb(results)
		if len(candidates) == 0 {
			log.Info().Str("person", person.Name).Msg("No candidate handles found")
			if i < len(persons)-1 {
				time.Sleep(1 * time.Second)
			}
			continue
		}

		log.Info().Str("person", person.Name).Strs("candidates", candidates).Msg("Found candidate handles")

		// Format search results for LLM
		var resultsText strings.Builder
		for j, r := range results {
			fmt.Fprintf(&resultsText, "%d. Title: %s\n   Snippet: %s\n   URL: %s\n", j+1, r.Title, r.Snippet, r.URL)
		}

		// Build LLM request to confirm the handle
		userPrompt := fmt.Sprintf(
			"Given these search results for \"%s twitter\":\n%s\nCandidate handles found: %s\n\nWhich Twitter/X handle is the official account of %s?\nReply with EXACTLY one line: HANDLE: @username\nOr if none found: HANDLE: NONE",
			person.Name, resultsText.String(), strings.Join(candidates, ", "), person.Name,
		)

		chatReq := llm.ChatRequest{
			Model:       model,
			Temperature: 0.1,
			MaxTokens:   16384,
			Messages: []llm.ChatMessage{
				{Role: "system", Content: "You are a Twitter handle verification assistant."},
				{Role: "user", Content: userPrompt},
			},
		}

		payloadBytes, err := json.Marshal(chatReq)
		if err != nil {
			log.Error().Err(err).Str("person", person.Name).Msg("Failed to marshal LLM request")
			continue
		}

		respBytes, err := client.DoRequest(ctx, apiURL, apiKey, payloadBytes)
		if err != nil {
			log.Error().Err(err).Str("person", person.Name).Msg("LLM request failed")
			continue
		}

		var chatResp llm.ChatResponse
		if err := json.Unmarshal(respBytes, &chatResp); err != nil {
			log.Error().Err(err).Str("person", person.Name).Msg("Failed to parse LLM response")
			continue
		}

		if chatResp.Error != nil {
			log.Error().Str("person", person.Name).Str("error", chatResp.Error.Message).Msg("LLM API returned error")
			continue
		}

		if len(chatResp.Choices) == 0 {
			log.Error().Str("person", person.Name).Msg("LLM returned no choices")
			continue
		}

		content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
		handleRe := regexp.MustCompile(`(?i)HANDLE:\s*@(\S+)`)
		match := handleRe.FindStringSubmatch(content)

		if match == nil || strings.EqualFold(match[1], "NONE") {
			log.Info().Str("person", person.Name).Str("llm_response", content).Msg("No confirmed handle")
			if i < len(persons)-1 {
				time.Sleep(1 * time.Second)
			}
			continue
		}

		confirmedHandle := strings.ToLower(match[1])
		log.Info().Str("person", person.Name).Str("handle", confirmedHandle).Bool("dry_run", opts.DryRun).Msg("Confirmed Twitter handle")

		if !opts.DryRun {
			if err := storer.AddTwitterHandle(ctx, person.ID, confirmedHandle); err != nil {
				log.Error().Err(err).Str("person", person.Name).Str("handle", confirmedHandle).Msg("Failed to save handle")
				continue
			}
			log.Info().Str("person", person.Name).Str("handle", confirmedHandle).Msg("Saved Twitter handle")
		}

		discovered++

		// Rate limit between persons
		if i < len(persons)-1 {
			time.Sleep(1 * time.Second)
		}
	}

	log.Info().Int("discovered", discovered).Int("total_persons", len(persons)).Msg("Twitter discovery completed")
	return nil
}

func runTwitterFacts(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
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
