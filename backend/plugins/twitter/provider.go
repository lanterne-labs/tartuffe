package twitter

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/promptrules"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	defaultMaxTweets    = 50
	defaultBatchSize    = 20
	defaultRevisitHours = 24 * 365 * 10 // 10 years — never re-process same tweet URL
	// twitterMaxConsecutiveFailures aborts a handle's extraction after this
	// many batches failed in a row: the LLM is down, not the tweets.
	twitterMaxConsecutiveFailures = 3
)

// pluginInstructions provides twitter-specific extraction instructions for the LLM.
const pluginInstructions = `## Twitter-Specific Instructions
The content below consists of tweets (social media posts). Apply these rules:
- Each tweet has a URL in the format "https://x.com/handle/status/...". Use this URL as the source_url for facts extracted from that tweet.
- Tweets use informal language — normalize to formal factual statements.
- Skip tweets that are purely opinions, jokes, memes, or personal greetings with no factual content.
- Skip retweets and quote-tweets that don't add original factual content.
- A single tweet may contain multiple facts — extract all of them.
- Hashtags and @mentions may provide context but are not facts by themselves.
- Dates mentioned in tweets should be interpreted relative to the tweet's timestamp.`

// Provider orchestrates tweet fetching and fact extraction for twitter handles.
// Storer inserts the facts; Store holds the plugin's handles and fetch log.
type Provider struct {
	Fetcher       TweetFetcher
	FactExtractor *categorizer.OpenAIExtractor
	Embedder      *embedding.OpenAIEmbeddingGenerator
	Storer        *storage.PostgresStorer
	Store         *Store
}

// CollectFacts fetches tweets for a handle and extracts facts.
func (p *Provider) CollectFacts(ctx context.Context, handle Handle, opts Options) (*Result, error) {
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
		alreadyFetched, err := p.Store.IsRangeFetched(ctx, handle.ID, dateFrom, dateTo)
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
			return result, p.extractLeftovers(ctx, handle, batchSize, opts, result, nil)
		}
	}

	// Fetch tweets
	fetchOpts := FetchOptions{
		MaxTweets: maxTweets,
		DateFrom:  &dateFrom,
		DateTo:    &dateTo,
	}

	query := "from:" + handle.Handle
	tweets, coveredFrom, err := p.fetchRange(ctx, query, fetchOpts, dateFrom)
	if err != nil {
		return result, p.extractLeftovers(ctx, handle, batchSize, opts, result,
			fmt.Errorf("failed to fetch tweets for @%s: %w", handle.Handle, err))
	}
	if coveredFrom.After(dateFrom) {
		// The range is not fully fetched: only the covered part is logged,
		// and the rest is fetched again by a later run.
		log.Warn().Str("handle", handle.Handle).Int("tweets", len(tweets)).
			Str("from", dateFrom.Format("2006-01-02")).Str("covered_from", coveredFrom.Format("2006-01-02")).
			Msg("twitter: range too dense to fetch whole; the part before covered_from stays unfetched")
		dateFrom = coveredFrom
	}
	// Nothing covered (a dense last day): nothing to log.
	logRange := dateFrom.Before(dateTo)

	result.TweetsFetched = len(tweets)
	log.Info().
		Str("handle", handle.Handle).
		Int("tweets_fetched", len(tweets)).
		Msg("twitter: fetch completed")

	if len(tweets) == 0 {
		// Still log the fetch so we don't re-query the same empty range
		if err := p.Store.LogFetch(ctx, handle.ID, dateFrom, dateTo, 0); err != nil {
			log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to log twitter fetch")
		}
		return result, p.extractLeftovers(ctx, handle, batchSize, opts, result, nil)
	}

	if opts.DryRun {
		for i, tw := range tweets {
			log.Info().
				Int("index", i+1).
				Str("url", tw.URL).
				Str("text", util.TruncateString(tw.Text, 100)).
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

	// Fetch step of the document state machine: every tweet to process is
	// saved now and the range logged as fetched, before any LLM call.
	docs := make([]core.SourceDocument, len(toProcess))
	for i, tw := range toProcess {
		docs[i] = documentFromTweet(tw, handle)
	}
	if _, err := p.Storer.SaveFetchedDocuments(ctx, docs); err != nil {
		return result, fmt.Errorf("saving tweets: %w", err)
	}
	if logRange {
		if err := p.Store.LogFetch(ctx, handle.ID, dateFrom, dateTo, result.TweetsFetched); err != nil {
			log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to log twitter fetch")
		}
		if err := p.Store.UpdateHandleLastFetched(ctx, handle.ID, dateTo); err != nil {
			log.Warn().Err(err).Int64("handle_id", handle.ID).Msg("Failed to update last_fetched_at")
		}
	}

	if err := p.extractStoredTweets(ctx, handle, batchSize, result); err != nil {
		return result, err
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

// maxFetchRounds bounds the fetches of one range: a round fetches the
// MaxTweets newest tweets before the end of what is left of the range.
const maxFetchRounds = 20

// fetchRange fetches every tweet of [opts.DateFrom, opts.DateTo). The API
// returns the newest tweets first and at most MaxTweets per fetch, so a range
// with more tweets than that is fetched in rounds, each one ending where the
// previous round stopped (the oldest tweet it returned). Returns the tweets
// and the start of the part of the range that is fully fetched: dateFrom
// when the whole range is, else the day after the oldest tweet fetched (a
// round could not be made before maxFetchRounds, or the fetch of a round
// failed after the first). An error in the first round is returned.
func (p *Provider) fetchRange(ctx context.Context, query string, opts FetchOptions, dateFrom time.Time) ([]Tweet, time.Time, error) {
	var all []Tweet
	seen := make(map[string]bool)
	until := *opts.DateTo
	for round := 0; round < maxFetchRounds; round++ {
		roundOpts := opts
		roundOpts.DateTo = &until
		tweets, err := p.Fetcher.FetchTweets(ctx, query, roundOpts)
		if err != nil {
			if round == 0 {
				return nil, dateFrom, err
			}
			log.Warn().Err(err).Int("round", round+1).Msg("twitter: fetch round failed; the part before it stays unfetched")
			return all, dayAfter(until), nil
		}
		var oldest *time.Time
		for _, tw := range tweets {
			if seen[tw.ID] {
				continue
			}
			seen[tw.ID] = true
			all = append(all, tw)
			if tw.CreatedAt != nil && (oldest == nil || tw.CreatedAt.Before(*oldest)) {
				oldest = tw.CreatedAt
			}
		}
		if len(tweets) < opts.MaxTweets || oldest == nil || !oldest.After(dateFrom) {
			return all, dateFrom, nil
		}
		// One second after the oldest, so that another tweet of the same
		// second is not lost; the oldest itself comes again and seen drops it.
		until = oldest.Add(time.Second)
	}
	return all, dayAfter(until), nil
}

// dayAfter returns the start of the day after t (UTC).
func dayAfter(t time.Time) time.Time {
	return t.UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
}

// extractStoredTweets is the extract step of the document state machine for
// the handle's person: every unfinished tweet — this run's and whatever an
// earlier run left fetched or failed — goes through the LLM in batches,
// embedding and dedup-insert. A failed batch fails each of its tweets; a
// streak of failures aborts the run (the LLM is down).
func (p *Provider) extractStoredTweets(ctx context.Context, handle Handle, batchSize int, result *Result) error {
	result.storedExtracted = true
	stanceMgr, err := core.NewStanceManagerFromEnv(p.Storer)
	if err != nil {
		return fmt.Errorf("stance manager required for fact ingestion: %w", err)
	}
	factInserter := core.NewFactInserter(p.Storer, stanceMgr)
	factInserter.EnableDedupe(p.Storer)

	extract := func(ctx context.Context, batchDocs []core.SourceDocument) (map[int64]int, error) {
		factsByDoc, err := p.extractTweetBatch(ctx, handle, factInserter, batchDocs, result)
		if err != nil {
			log.Warn().Err(err).Str("handle", handle.Handle).Int("batch_size", len(batchDocs)).
				Msg("twitter: failed to extract facts from batch")
			result.Errors = append(result.Errors, err)
			metrics.IncrementTweets("error")
			return nil, err
		}
		return factsByDoc, nil
	}

	stats, err := core.ProcessDocuments(ctx, p.Storer, "twitter", handle.PersonID, extract, core.ProcessOptions{
		BatchSize:              batchSize,
		MaxConsecutiveFailures: twitterMaxConsecutiveFailures,
	})
	log.Info().Str("handle", handle.Handle).Int("pending", stats.Pending).Int("extracted", stats.Extracted).
		Int("failed", stats.Failed).Int("facts", stats.Facts).Msg("twitter: documents extracted")
	return err
}

// extractTweetBatch runs the LLM extraction on one batch of stored tweets,
// embeds and dedup-inserts the facts, and returns the facts produced per
// document. An extraction failure fails the batch, and so does an embedding
// or insert failure that core.FailsDocument reports (an unreachable LLM): its
// tweets stay pending, facts inserted before the failure stay, and the
// retry's re-extracted restatements of them are grouped by the inserter's
// same-claim check. Any other failure of a single fact skips that fact and is
// counted in result.Errors.
func (p *Provider) extractTweetBatch(ctx context.Context, handle Handle, factInserter *core.FactInserter, batchDocs []core.SourceDocument, result *Result) (map[int64]int, error) {
	batch := make([]Tweet, len(batchDocs))
	idByURL := make(map[string]int64, len(batchDocs))
	for j, d := range batchDocs {
		batch[j] = tweetFromDocument(d)
		idByURL[d.URL] = d.ID
	}

	facts, err := p.FactExtractor.ExtractFactsWithConfig(ctx, promptrules.FactExtractorConfig{
		TargetName:         handle.PersonName,
		SourceURL:          fmt.Sprintf("https://x.com/%s", handle.Handle),
		Content:            formatTweetBatch(batch),
		PluginInstructions: pluginInstructions,
	})
	if err != nil {
		return nil, err
	}

	result.TweetsProcessed += len(batch)
	if len(facts) == 0 {
		result.TweetsProcessedNoFacts += len(batch)
		metrics.IncrementTweets("parsed_no_facts")
	} else {
		metrics.IncrementTweets("parsed")
	}

	factsByDoc := make(map[int64]int, len(batchDocs))
	for _, fact := range facts {
		sourceURL := matchFactToTweetURL(fact, batch)

		embeddings, err := p.Embedder.GenerateEmbeddings(ctx, embedding.PurposeFactStorage, []string{fact.EmbeddingText})
		if core.FailsDocument(ctx, err) {
			return nil, fmt.Errorf("embedding a fact of %s: %w", sourceURL, err)
		}
		if err != nil || len(embeddings) == 0 || len(embeddings[0]) == 0 {
			log.Warn().Err(err).Str("fact", util.TruncateString(fact.NormalizedFactText, 80)).Msg("twitter: failed to generate embedding")
			result.Errors = append(result.Errors, fmt.Errorf("embedding failed: %w", err))
			continue
		}

		// Publication date — the tweet API's CreatedAt is authoritative;
		// a date the LLM read in the body is only a fallback.
		tweetCreatedAt := matchFactToTweetCreatedAt(fact, batch)
		finalPubAt := core.PickPublishedAt(tweetCreatedAt, fact.ArticlePublishedAt, fact.PublishedAt)

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
			PublishedAt:        finalPubAt,
			SourceType:         "twitter",
			Lang:               factLang(fact, batch),
			Metadata: map[string]any{
				"twitter_handle": handle.Handle,
				"person_name":    handle.PersonName,
			},
		}

		insertResult, err := factInserter.InsertFacts(ctx, []core.StoreFactInput{input})
		if core.FailsDocument(ctx, err) {
			return nil, fmt.Errorf("storing a fact of %s: %w", sourceURL, err)
		}
		if err != nil {
			log.Warn().Err(err).Str("fact", util.TruncateString(fact.NormalizedFactText, 80)).Msg("twitter: failed to store fact")
			result.Errors = append(result.Errors, err)
			continue
		}
		result.FactsExtracted += insertResult.Inserted
		factsByDoc[idByURL[sourceURL]] += insertResult.Inserted + insertResult.Skipped
		metrics.IncrementTweets("fact_extracted")
	}
	return factsByDoc, nil
}

// extractLeftovers is CollectFacts' exit when the run fetched nothing new: the
// person's tweets an earlier run left unfinished (an LLM outage mid-run) are
// extracted anyway, so they never wait for new tweets to show up. A dry run
// makes no LLM call, and a window of a windowed collection leaves them to the
// collection (skipLeftovers). fetchErr, when set, is returned along with any
// extraction error.
func (p *Provider) extractLeftovers(ctx context.Context, handle Handle, batchSize int, opts Options, result *Result, fetchErr error) error {
	if opts.DryRun || opts.skipLeftovers {
		return fetchErr
	}
	return errors.Join(fetchErr, p.extractStoredTweets(ctx, handle, batchSize, result))
}

// documentFromTweet is the stored form of a fetched tweet.
func documentFromTweet(tw Tweet, handle Handle) core.SourceDocument {
	return core.SourceDocument{
		SourceType:  "twitter",
		URL:         tw.URL,
		PersonID:    handle.PersonID,
		Title:       "",
		Content:     tw.Text,
		PublishedAt: tw.CreatedAt,
		Metadata: map[string]any{
			"tweet_id":      tw.ID,
			"author_handle": tw.AuthorHandle,
			"author_name":   tw.AuthorName,
			"lang":          tw.Lang,
		},
	}
}

// tweetFromDocument rebuilds the tweet the extraction prompt needs from its
// stored form.
func tweetFromDocument(d core.SourceDocument) Tweet {
	str := func(k string) string { v, _ := d.Metadata[k].(string); return v }
	return Tweet{
		ID:           str("tweet_id"),
		Text:         d.Content,
		AuthorHandle: str("author_handle"),
		AuthorName:   str("author_name"),
		URL:          d.URL,
		Lang:         str("lang"),
		CreatedAt:    d.PublishedAt,
	}
}

// CollectFactsWithDateWindows breaks the date range into N-day windows
// and calls CollectFacts for each, skipping already-fetched windows. A failed
// window is collected in the result, except an unreachable LLM
// (llm.ErrUnavailable), which stops the collection and is returned. The
// person's tweets an earlier run left unfinished are extracted once, after
// the windows, when no window extracted any.
func (p *Provider) CollectFactsWithDateWindows(ctx context.Context, handle Handle, opts Options) (*Result, error) {
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
		windowOpts.skipLeftovers = true

		result, err := p.CollectFacts(ctx, handle, windowOpts)
		if result != nil && result.storedExtracted {
			aggregate.storedExtracted = true
		}
		if err != nil && ctx.Err() != nil {
			return aggregate, fmt.Errorf("window %d/%d stopped: %w", i+1, len(windows), ctx.Err())
		}
		if errors.Is(err, llm.ErrUnavailable) {
			return aggregate, fmt.Errorf("window %d/%d (%s to %s): %w",
				i+1, len(windows), w.from.Format("2006-01-02"), w.to.Format("2006-01-02"), err)
		}
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

	if !aggregate.storedExtracted {
		batchSize := opts.BatchSize
		if batchSize <= 0 {
			batchSize = defaultBatchSize
		}
		if err := p.extractLeftovers(ctx, handle, batchSize, opts, aggregate, nil); err != nil {
			return aggregate, err
		}
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
func determineDateRange(handle Handle, opts Options) (time.Time, time.Time) {
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

// factLang is the language of the fact text: the one the extractor gives,
// else the tweet's. Twitter's lang is a guess about the tweet, not the fact,
// and is often no language at all (qme: media only, zxx: no text, und:
// undetermined): 279 French facts carried such codes or "it"/"es".
func factLang(fact backend.Fact, batch []Tweet) string {
	if fact.Lang != "" {
		return fact.Lang
	}
	if tw := findMatchingTweet(fact, batch); tw != nil && isLanguageCode(tw.Lang) {
		return tw.Lang
	}
	return ""
}

// isLanguageCode reports whether a Twitter lang names a language: not one of
// Twitter's own codes (q*, zxx, und).
func isLanguageCode(lang string) bool {
	return lang != "" && lang != "und" && lang != "zxx" && !strings.HasPrefix(lang, "q")
}

// matchFactToTweetCreatedAt returns the CreatedAt of the tweet matching a fact's
// SourceURL. Falls back to the first tweet's CreatedAt. Returns nil only when
// the batch is empty or the matched tweet has no timestamp. This is the
// primary structured pub-date source for Twitter facts.
func matchFactToTweetCreatedAt(fact backend.Fact, batch []Tweet) *time.Time {
	if tw := findMatchingTweet(fact, batch); tw != nil {
		return tw.CreatedAt
	}
	return nil
}

// findMatchingTweet returns the tweet whose URL matches the fact's SourceURL,
// or the first tweet in the batch as a fallback. Returns nil only when the
// batch itself is empty. Shared by the per-field matchFactToTweet* helpers.
func findMatchingTweet(fact backend.Fact, batch []Tweet) *Tweet {
	if fact.SourceURL != "" {
		for i := range batch {
			if batch[i].URL == fact.SourceURL {
				return &batch[i]
			}
		}
	}
	if len(batch) > 0 {
		return &batch[0]
	}
	return nil
}
