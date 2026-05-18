package twitter

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/storage"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	defaultMaxTweets    = 50
	defaultBatchSize    = 20
	defaultRevisitHours = 24 * 365 * 10 // 10 years — never re-process same tweet URL
)

// PluginInstructions provides twitter-specific extraction instructions for the LLM.
const PluginInstructions = `## Twitter-Specific Instructions
The content below consists of tweets (social media posts). Apply these rules:
- Each tweet has a URL in the format "https://x.com/handle/status/...". Use this URL as the source_url for facts extracted from that tweet.
- Tweets use informal language — normalize to formal factual statements.
- Skip tweets that are purely opinions, jokes, memes, or personal greetings with no factual content.
- Skip retweets and quote-tweets that don't add original factual content.
- A single tweet may contain multiple facts — extract all of them.
- Hashtags and @mentions may provide context but are not facts by themselves.
- Dates mentioned in tweets should be interpreted relative to the tweet's timestamp.`

// Provider orchestrates tweet fetching and fact extraction for twitter handles.
type Provider struct {
	Fetcher       TweetFetcher
	FactExtractor *categorizer.OpenAIExtractor
	Embedder      *embedding.OpenAIEmbeddingGenerator
	Storer        *storage.PostgresStorer
}

// CollectFacts fetches tweets for a handle and extracts facts.
func (p *Provider) CollectFacts(ctx context.Context, handle storage.TwitterHandle, opts Options) (*Result, error) {
	result := &Result{
		Handle:     handle.Handle,
		PersonName: handle.PersonName,
	}

	maxTweets := opts.MaxTweets
	if maxTweets <= 0 {
		maxTweets = defaultMaxTweets
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}
	revisitHours := opts.RevisitHours
	if revisitHours <= 0 {
		revisitHours = defaultRevisitHours
	}

	// Determine date range
	dateFrom, dateTo := determineDateRange(handle, opts)

	// Check fetch log (API-level dedup) unless --force
	if !opts.Force {
		alreadyFetched, err := p.Storer.IsTwitterRangeFetched(ctx, handle.ID, dateFrom, dateTo)
		if err != nil {
			return nil, fmt.Errorf("failed to check fetch log: %w", err)
		}
		if alreadyFetched {
			log.Info().
				Str("handle", handle.Handle).
				Str("from", dateFrom.Format("2006-01-02")).
				Str("to", dateTo.Format("2006-01-02")).
				Msg("twitter: range already fetched, skipping")
			result.SkippedAlreadyFetchedRange = true
			return result, nil
		}
	}

	// Fetch tweets
	fetchOpts := FetchOptions{
		MaxTweets: maxTweets,
		DateFrom:  &dateFrom,
		DateTo:    &dateTo,
	}

	query := "from:" + handle.Handle
	tweets, err := p.Fetcher.FetchTweets(ctx, query, fetchOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tweets for @%s: %w", handle.Handle, err)
	}

	result.TweetsFetched = len(tweets)
	log.Info().
		Str("handle", handle.Handle).
		Int("tweets_fetched", len(tweets)).
		Msg("twitter: fetch completed")

	if len(tweets) == 0 {
		// Still log the fetch so we don't re-query the same empty range
		if err := p.Storer.LogTwitterFetch(ctx, handle.ID, dateFrom, dateTo, 0); err != nil {
			log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to log twitter fetch")
		}
		return result, nil
	}

	if opts.DryRun {
		for i, tw := range tweets {
			log.Info().
				Int("index", i+1).
				Str("url", tw.URL).
				Str("text", truncate(tw.Text, 100)).
				Bool("retweet", tw.IsRetweet).
				Msg("twitter: [DRY-RUN] would process")
		}
		return result, nil
	}

	// Filter: skip retweets and already-processed tweets
	var toProcess []Tweet
	for _, tw := range tweets {
		if tw.IsRetweet {
			result.SkippedRetweets++
			metrics.IncrementTweets("skipped_retweet")
			continue
		}

		if tw.URL != "" && !opts.Force {
			visitable, _, err := p.Storer.IsURLVisitable(ctx, tw.URL, revisitHours)
			if err != nil {
				log.Warn().Err(err).Str("url", tw.URL).Msg("Failed to check URL visitability")
				result.Errors = append(result.Errors, err)
				continue
			}
			if !visitable {
				result.SkippedAlreadyProcessed++
				metrics.IncrementTweets("skipped_already_processed")
				continue
			}
		}

		toProcess = append(toProcess, tw)
	}

	log.Info().
		Str("handle", handle.Handle).
		Int("to_process", len(toProcess)).
		Int("skipped_retweets", result.SkippedRetweets).
		Int("skipped_already_processed", result.SkippedAlreadyProcessed).
		Msg("twitter: filtered tweets")

	if len(toProcess) == 0 {
		if err := p.Storer.LogTwitterFetch(ctx, handle.ID, dateFrom, dateTo, result.TweetsFetched); err != nil {
			log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to log twitter fetch")
		}
		return result, nil
	}

	// Process in batches
	stanceMgr, err := core.NewStanceManagerFromEnv(storage.NewStanceStorerAdapter(p.Storer))
	if err != nil {
		return result, fmt.Errorf("stance manager required for fact ingestion: %w", err)
	}
	factInserter := core.NewFactInserter(p.Storer, stanceMgr)

	for i := 0; i < len(toProcess); i += batchSize {
		end := i + batchSize
		if end > len(toProcess) {
			end = len(toProcess)
		}
		batch := toProcess[i:end]

		batchContent := formatTweetBatch(batch)

		cfg := core.FactExtractorConfig{
			TargetName:        handle.PersonName,
			SourceURL:         fmt.Sprintf("https://x.com/%s", handle.Handle),
			Content:           batchContent,
			PluginInstructions: PluginInstructions,
		}

		facts, err := p.FactExtractor.ExtractFactsWithConfig(ctx, cfg)
		if err != nil {
			log.Warn().Err(err).
				Str("handle", handle.Handle).
				Int("batch_start", i).
				Int("batch_size", len(batch)).
				Msg("twitter: failed to extract facts from batch")
			result.Errors = append(result.Errors, err)
			metrics.IncrementTweets("error")
			continue
		}

		result.TweetsProcessed += len(batch)

		if len(facts) == 0 {
			result.TweetsProcessedNoFacts += len(batch)
			metrics.IncrementTweets("parsed_no_facts")
		} else {
			metrics.IncrementTweets("parsed")
		}

		// Store each extracted fact
		for _, fact := range facts {
			sourceURL := matchFactToTweetURL(fact, batch)

			embeddings, err := p.Embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, []string{fact.EmbeddingText})
			if err != nil || len(embeddings) == 0 || len(embeddings[0]) == 0 {
				log.Warn().Err(err).Str("fact", truncate(fact.NormalizedFactText, 80)).Msg("twitter: failed to generate embedding")
				result.Errors = append(result.Errors, fmt.Errorf("embedding failed: %w", err))
				continue
			}

			input := core.StoreFactInput{
				SubjectEntity:      fact.SubjectEntity,
				SubjectEntityType:  fact.SubjectEntityType,
				NormalizedFactText: fact.NormalizedFactText,
				EmbeddingText:      fact.EmbeddingText,
				Embedding:          embeddings[0],
				OriginalQuote:      fact.OriginalQuote,
				CanonicalVerb:      fact.CanonicalVerb,
				InvolvedPeople:     fact.InvolvedPeople,
				SourceURL:          sourceURL,
				PublishedAt:        fact.PublishedAt,
				SourceType:         "twitter",
				Lang:               matchFactToTweetLang(fact, batch),
				Metadata: map[string]any{
					"twitter_handle": handle.Handle,
					"person_name":    handle.PersonName,
				},
			}

			insertResult, err := factInserter.InsertFacts(ctx, []core.StoreFactInput{input})
			if err != nil {
				log.Warn().Err(err).
					Str("fact", truncate(fact.NormalizedFactText, 80)).
					Msg("twitter: failed to store fact")
				result.Errors = append(result.Errors, err)
				continue
			}

			result.FactsExtracted += insertResult.Inserted
			metrics.IncrementTweets("fact_extracted")
		}
	}

	// Log the fetch
	if err := p.Storer.LogTwitterFetch(ctx, handle.ID, dateFrom, dateTo, result.TweetsFetched); err != nil {
		log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to log twitter fetch")
	}

	// Update last_fetched_at
	if err := p.Storer.UpdateTwitterHandleLastFetched(ctx, handle.ID, time.Now()); err != nil {
		log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to update last_fetched_at")
	}

	log.Info().
		Str("handle", handle.Handle).
		Int("tweets_processed", result.TweetsProcessed).
		Int("tweets_no_facts", result.TweetsProcessedNoFacts).
		Int("facts_extracted", result.FactsExtracted).
		Int("errors", len(result.Errors)).
		Msg("twitter: collection completed")

	return result, nil
}

// CollectContent fetches tweets for a handle and returns them as BatchContentItems
// without calling any LLM. Each tweet becomes its own content item with PluginInstructions set.
// Use this to submit tweets to the Gemini batch API instead of sync extraction.
func (p *Provider) CollectContent(ctx context.Context, handle storage.TwitterHandle, opts Options) ([]core.BatchContentItem, error) {
	maxTweets := opts.MaxTweets
	if maxTweets <= 0 {
		maxTweets = defaultMaxTweets
	}
	revisitHours := opts.RevisitHours
	if revisitHours <= 0 {
		revisitHours = defaultRevisitHours
	}

	dateFrom, dateTo := determineDateRange(handle, opts)

	// Check fetch log (API-level dedup) unless --force
	if !opts.Force {
		alreadyFetched, err := p.Storer.IsTwitterRangeFetched(ctx, handle.ID, dateFrom, dateTo)
		if err != nil {
			return nil, fmt.Errorf("failed to check fetch log: %w", err)
		}
		if alreadyFetched {
			log.Info().
				Str("handle", handle.Handle).
				Str("from", dateFrom.Format("2006-01-02")).
				Str("to", dateTo.Format("2006-01-02")).
				Msg("twitter: range already fetched, skipping")
			return nil, nil
		}
	}

	// Fetch tweets
	fetchOpts := FetchOptions{
		MaxTweets: maxTweets,
		DateFrom:  &dateFrom,
		DateTo:    &dateTo,
	}

	query := "from:" + handle.Handle
	tweets, err := p.Fetcher.FetchTweets(ctx, query, fetchOpts)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch tweets for @%s: %w", handle.Handle, err)
	}

	log.Info().
		Str("handle", handle.Handle).
		Int("tweets_fetched", len(tweets)).
		Msg("twitter: fetch completed (batch mode)")

	if len(tweets) == 0 {
		if err := p.Storer.LogTwitterFetch(ctx, handle.ID, dateFrom, dateTo, 0); err != nil {
			log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to log twitter fetch")
		}
		return nil, nil
	}

	// Filter: skip retweets and already-processed tweets
	var toProcess []Tweet
	for _, tw := range tweets {
		if tw.IsRetweet {
			continue
		}
		if tw.URL != "" && !opts.Force {
			visitable, _, err := p.Storer.IsURLVisitable(ctx, tw.URL, revisitHours)
			if err != nil {
				log.Warn().Err(err).Str("url", tw.URL).Msg("Failed to check URL visitability")
				continue
			}
			if !visitable {
				continue
			}
		}
		toProcess = append(toProcess, tw)
	}

	if len(toProcess) == 0 {
		if err := p.Storer.LogTwitterFetch(ctx, handle.ID, dateFrom, dateTo, len(tweets)); err != nil {
			log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to log twitter fetch")
		}
		return nil, nil
	}

	// Convert each tweet to a BatchContentItem
	var items []core.BatchContentItem
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = defaultBatchSize
	}

	for i := 0; i < len(toProcess); i += batchSize {
		end := i + batchSize
		if end > len(toProcess) {
			end = len(toProcess)
		}
		batch := toProcess[i:end]
		content := formatTweetBatch(batch)

		// Use the first tweet's URL as source; individual tweet URLs are in the content
		sourceURL := fmt.Sprintf("https://x.com/%s", handle.Handle)

		items = append(items, core.BatchContentItem{
			SourceURL:          sourceURL,
			Content:            content,
			TargetName:         handle.PersonName,
			TargetType:         "person",
			PublishedAt:        batch[0].CreatedAt,
			PluginInstructions: PluginInstructions,
		})
	}

	// Log the fetch
	if err := p.Storer.LogTwitterFetch(ctx, handle.ID, dateFrom, dateTo, len(tweets)); err != nil {
		log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to log twitter fetch")
	}
	if err := p.Storer.UpdateTwitterHandleLastFetched(ctx, handle.ID, time.Now()); err != nil {
		log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to update last_fetched_at")
	}

	log.Info().
		Str("handle", handle.Handle).
		Int("tweets", len(toProcess)).
		Int("batch_items", len(items)).
		Msg("twitter: content collected for batch submission")

	return items, nil
}

// CollectFactsWithDateWindows breaks the date range into N-day windows
// and calls CollectFacts for each, skipping already-fetched windows.
func (p *Provider) CollectFactsWithDateWindows(ctx context.Context, handle storage.TwitterHandle, opts Options) (*Result, error) {
	if opts.WindowDays <= 0 {
		return nil, fmt.Errorf("WindowDays must be > 0, got %d", opts.WindowDays)
	}

	dateFrom, dateTo := determineDateRange(handle, opts)

	if !dateFrom.Before(dateTo) {
		return nil, fmt.Errorf("DateFrom (%s) must be before DateTo (%s)",
			dateFrom.Format("2006-01-02"), dateTo.Format("2006-01-02"))
	}

	// Generate windows
	type window struct{ from, to time.Time }
	var windows []window
	cursor := dateFrom
	for cursor.Before(dateTo) {
		end := cursor.AddDate(0, 0, opts.WindowDays)
		if end.After(dateTo) {
			end = dateTo
		}
		windows = append(windows, window{from: cursor, to: end})
		cursor = end
	}

	log.Info().
		Str("handle", handle.Handle).
		Int("windows", len(windows)).
		Int("window_days", opts.WindowDays).
		Str("from", dateFrom.Format("2006-01-02")).
		Str("to", dateTo.Format("2006-01-02")).
		Msg("twitter: starting windowed collection")

	aggregate := &Result{
		Handle:     handle.Handle,
		PersonName: handle.PersonName,
	}

	for i, w := range windows {
		select {
		case <-ctx.Done():
			log.Warn().Str("handle", handle.Handle).Int("window", i+1).Msg("Context cancelled, returning partial results")
			aggregate.Errors = append(aggregate.Errors, ctx.Err())
			return aggregate, ctx.Err()
		default:
		}

		log.Info().
			Str("handle", handle.Handle).
			Int("window", i+1).
			Int("total", len(windows)).
			Str("from", w.from.Format("2006-01-02")).
			Str("to", w.to.Format("2006-01-02")).
			Msg("twitter: processing window")

		windowOpts := opts
		windowFrom := w.from
		windowTo := w.to
		windowOpts.DateFrom = &windowFrom
		windowOpts.DateTo = &windowTo
		windowOpts.WindowDays = 0 // prevent recursion

		result, err := p.CollectFacts(ctx, handle, windowOpts)
		if err != nil {
			log.Error().Err(err).
				Str("handle", handle.Handle).
				Int("window", i+1).
				Msg("twitter: window failed, continuing")
			aggregate.Errors = append(aggregate.Errors, fmt.Errorf("window %d/%d (%s to %s): %w",
				i+1, len(windows), w.from.Format("2006-01-02"), w.to.Format("2006-01-02"), err))
			continue
		}

		aggregateResult(aggregate, result)
	}

	log.Info().
		Str("handle", handle.Handle).
		Int("windows", len(windows)).
		Int("tweets_fetched", aggregate.TweetsFetched).
		Int("facts_extracted", aggregate.FactsExtracted).
		Msg("twitter: windowed collection completed")

	return aggregate, nil
}

// determineDateRange resolves the effective date range from options and handle state.
func determineDateRange(handle storage.TwitterHandle, opts Options) (time.Time, time.Time) {
	now := time.Now()

	var dateFrom time.Time
	if opts.DateFrom != nil {
		dateFrom = *opts.DateFrom
	} else if handle.LastFetchedAt != nil {
		dateFrom = *handle.LastFetchedAt
	} else {
		// Default: 30 days back
		dateFrom = now.AddDate(0, 0, -30)
	}

	dateTo := now
	if opts.DateTo != nil {
		dateTo = *opts.DateTo
	}

	return dateFrom, dateTo
}

// formatTweetBatch formats a slice of tweets into a text block for the LLM.
func formatTweetBatch(tweets []Tweet) string {
	var sb strings.Builder
	for i, tw := range tweets {
		if i > 0 {
			sb.WriteString("\n---\n")
		}
		sb.WriteString(fmt.Sprintf("Tweet URL: %s\n", tw.URL))
		sb.WriteString(fmt.Sprintf("Author: @%s (%s)\n", tw.AuthorHandle, tw.AuthorName))
		if tw.CreatedAt != nil {
			sb.WriteString(fmt.Sprintf("Date: %s\n", tw.CreatedAt.Format("2006-01-02 15:04")))
		}
		if tw.Lang != "" {
			sb.WriteString(fmt.Sprintf("Language: %s\n", tw.Lang))
		}
		sb.WriteString(fmt.Sprintf("Text: %s\n", tw.Text))
	}
	return sb.String()
}

// matchFactToTweetURL tries to match a fact's source URL to a tweet in the batch.
// Falls back to the first tweet's URL if no match is found.
func matchFactToTweetURL(fact backend.Fact, batch []Tweet) string {
	// If the LLM set SourceURL, try to match it
	if fact.SourceURL != "" {
		for _, tw := range batch {
			if tw.URL == fact.SourceURL {
				return tw.URL
			}
		}
		// LLM set a URL but it's not in our batch — use it anyway (it might be correct)
		return fact.SourceURL
	}

	// Fallback: use the first tweet's URL
	if len(batch) > 0 {
		return batch[0].URL
	}
	return ""
}

// matchFactToTweetLang returns the lang of the tweet matching a fact's SourceURL.
// Falls back to the first tweet's lang, or "" if none available.
func matchFactToTweetLang(fact backend.Fact, batch []Tweet) string {
	if fact.SourceURL != "" {
		for _, tw := range batch {
			if tw.URL == fact.SourceURL {
				return tw.Lang
			}
		}
	}
	if len(batch) > 0 {
		return batch[0].Lang
	}
	return ""
}

// truncate truncates a string to maxLen characters, adding "..." if truncated.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
