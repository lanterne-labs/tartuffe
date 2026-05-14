package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"digital-mirror/backend/alerting"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/objectstore"
	"digital-mirror/backend/plugins/frpolitics"
	"digital-mirror/backend/plugins/news"
	"digital-mirror/backend/plugins/photos"
	"digital-mirror/backend/plugins/twitter"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// pipelineCmd is the parent group for pipeline-related CLI commands. Subcommands
// live in their own files (e.g. cmd/pipeline_daily.go).
var pipelineCmd = &cobra.Command{
	Use:   "pipeline",
	Short: "Commands for the daily ingestion pipeline.",
	Long:  `Manually trigger the daily pipeline that's normally driven by the host cron.`,
}

func init() {
	rootCmd.AddCommand(pipelineCmd)
}

// runDailyPipeline executes the unified daily pipeline once and returns an
// aggregated error if any step failed. Designed to be invoked by the
// `dm pipeline daily` CLI command (which the host cron triggers).
//
// Steps in order:
//  1. news fact extraction      — queue-scoped, or all persons if DAILY_FOR_ALL=1
//  2. twitter fact extraction   — discovery (all) + collection (queue-scoped, or all if DAILY_FOR_ALL=1)
//  3. frpolitics                — votes + news for French deputies
//  4. contradiction detection   — over all persons with new pairs
//  5. photo backfill            — persons + parties missing an image
func runDailyPipeline(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier) error {
	start := time.Now()
	allPersons := os.Getenv("DAILY_FOR_ALL") == "1"
	log.Info().Bool("daily_for_all", allPersons).Msg("Daily pipeline started")

	// Resolve target persons for fact-extraction steps (news, twitter).
	targets, targetsErr := dailyTargets(ctx, storer)
	if targetsErr != nil {
		log.Error().Err(targetsErr).Msg("Daily pipeline: failed to resolve target persons")
	}
	targetIDs := make(map[int64]bool, len(targets))
	for _, t := range targets {
		targetIDs[t.PersonID] = true
	}
	log.Info().Int("targets", len(targets)).Msg("Daily pipeline: target persons resolved")

	// Step 1: News fact extraction. In batch mode, this also bundles tweets for the
	// same target set so they go through the same batch LLM job.
	stats, batchErr := collectAndSubmitBatch(ctx, storer, targets, targetIDs)
	if batchErr != nil {
		log.Error().Err(batchErr).Msg("Daily pipeline: news/batch submission failed")
	}

	// Step 2: Twitter — discovery (always all persons missing a handle) +
	// fact collection (queue-scoped, or all if DAILY_FOR_ALL=1).
	discoverErr := runTwitterDiscovery(ctx, storer)
	if discoverErr != nil {
		log.Error().Err(discoverErr).Msg("Daily pipeline: Twitter discovery failed")
	}

	var twitterErr error
	if os.Getenv("BATCH_PROVIDER") == "" {
		twitterErr = runTwitterCollection(ctx, storer, targetIDs)
		if twitterErr != nil {
			log.Error().Err(twitterErr).Msg("Daily pipeline: Twitter collection failed")
		}
	} else {
		log.Info().Msg("Twitter collection handled via batch submission, skipping sync path")
	}

	// Step 3: frpolitics (votes + news for deputies). Independent target set.
	fpErr := runFrpoliticsStep(ctx, storer)
	if fpErr != nil {
		log.Error().Err(fpErr).Msg("Daily pipeline: frpolitics failed")
	}

	// Step 4: Stance merge sweep. Looks for stances that should be merged per
	// LLM-vetted centroid clustering, applies the merges, and re-detects
	// contradictions across previously-separated facts. Catches the cross-stance
	// contradictions our per-fact assignTopic missed by mis-grouping.
	mergeErr := runStancesMergeStep(ctx, storer)
	if mergeErr != nil {
		log.Error().Err(mergeErr).Msg("Daily pipeline: stance merge sweep failed")
	}

	// Step 5: Contradiction detection. Always run over unchecked pairs across
	// all persons. In batch mode this submits a contradiction batch and returns;
	// the batch poller eventually finalizes results.
	contraErr := executeContradictionDetection(ctx, storer, ContradictionDetectOpts{})
	if contraErr != nil {
		log.Error().Err(contraErr).Msg("Daily pipeline: contradiction detection failed")
	}

	// Step 6: Photos backfill (persons + parties missing an image).
	photosErr := runPhotosBackfill(ctx, storer)
	if photosErr != nil {
		log.Error().Err(photosErr).Msg("Daily pipeline: photos backfill failed")
	}

	elapsed := time.Since(start)

	// Notify via Telegram
	if notifier != nil {
		hasErrors := targetsErr != nil || batchErr != nil || discoverErr != nil ||
			twitterErr != nil || fpErr != nil || mergeErr != nil || contraErr != nil || photosErr != nil
		hasWarnings := stats != nil && (stats.ScrapeErrors > 0 || len(stats.CollectErrors) > 0)
		noContent := stats != nil && stats.TargetsProcessed > 0 && stats.ContentItems == 0

		var msg string
		if hasErrors {
			var parts []string
			if targetsErr != nil {
				parts = append(parts, "targets: "+targetsErr.Error())
			}
			if batchErr != nil {
				parts = append(parts, "news/batch: "+batchErr.Error())
			}
			if discoverErr != nil {
				parts = append(parts, "twitter-discover: "+discoverErr.Error())
			}
			if twitterErr != nil {
				parts = append(parts, "twitter: "+twitterErr.Error())
			}
			if fpErr != nil {
				parts = append(parts, "frpolitics: "+fpErr.Error())
			}
			if mergeErr != nil {
				parts = append(parts, "stance-merge: "+mergeErr.Error())
			}
			if contraErr != nil {
				parts = append(parts, "contradictions: "+contraErr.Error())
			}
			if photosErr != nil {
				parts = append(parts, "photos: "+photosErr.Error())
			}
			msg = fmt.Sprintf("❌ <b>Daily pipeline errors</b> (%s)\n%s",
				elapsed.Round(time.Second), strings.Join(parts, "\n"))
		} else if noContent {
			msg = fmt.Sprintf("⚠️ <b>Daily pipeline completed with no content</b> (%s)",
				elapsed.Round(time.Second))
		} else if hasWarnings {
			msg = fmt.Sprintf("⚠️ <b>Daily pipeline completed with warnings</b> (%s)",
				elapsed.Round(time.Second))
		} else {
			msg = fmt.Sprintf("✅ <b>Daily pipeline completed</b> (%s)",
				elapsed.Round(time.Second))
		}

		if stats != nil {
			msg += "\n" + stats.summary()
		}

		if err := notifier.Send(ctx, msg); err != nil {
			log.Error().Err(err).Msg("Failed to send pipeline Telegram notification")
		}
	}

	// Aggregate step errors so the caller (the host cron) can exit non-zero.
	var stepErrs []error
	for _, e := range []error{targetsErr, batchErr, discoverErr, twitterErr, fpErr, mergeErr, contraErr, photosErr} {
		if e != nil {
			stepErrs = append(stepErrs, e)
		}
	}

	log.Info().
		Dur("elapsed", elapsed).
		Int("step_errors", len(stepErrs)).
		Msg("Daily pipeline finished")

	return errors.Join(stepErrs...)
}

// dailyTargets returns the persons that fact-extraction steps (news, twitter)
// should iterate over. Defaults to the search_queue contents (the "main persons
// queue"); when DAILY_FOR_ALL=1 is set, returns every person in the DB instead.
// Photos + frpolitics + contradictions ignore this filter — they have their own scope.
func dailyTargets(ctx context.Context, storer *storage.PostgresStorer) ([]storage.QueueEntry, error) {
	if os.Getenv("DAILY_FOR_ALL") == "1" {
		all, err := storer.GetAllPersons(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list all persons: %w", err)
		}
		out := make([]storage.QueueEntry, len(all))
		for i, p := range all {
			out[i] = storage.QueueEntry{
				PersonID:   p.ID,
				Name:       p.Name,
				EntityType: p.EntityType,
			}
		}
		return out, nil
	}
	return storer.GetSearchQueue(ctx)
}

// pipelineStats tracks aggregate stats across all targets in a pipeline run.
type pipelineStats struct {
	TargetsProcessed int
	ArticlesParsed   int
	ContentItems     int
	ScrapeErrors     int
	CollectErrors    []string // per-target collection errors
}

func (s pipelineStats) summary() string {
	msg := fmt.Sprintf("targets=%d articles=%d items=%d scrape_errors=%d",
		s.TargetsProcessed, s.ArticlesParsed, s.ContentItems, s.ScrapeErrors)
	if len(s.CollectErrors) > 0 {
		msg += fmt.Sprintf("\n⚠️ Collection errors (%d):", len(s.CollectErrors))
		for _, e := range s.CollectErrors {
			msg += "\n  • " + e
		}
	}
	return msg
}

// collectAndSubmitBatch searches news for the given target persons, extracts
// article content, and submits everything (plus tweets for the same targets)
// as a single Gemini batch job. Returns immediately — the batch poller handles
// retrieval and fact insertion asynchronously.
func collectAndSubmitBatch(ctx context.Context, storer *storage.PostgresStorer, targets []storage.QueueEntry, targetIDs map[int64]bool) (*pipelineStats, error) {
	stats := &pipelineStats{}

	if len(targets) == 0 {
		log.Info().Msg("No target persons for fact extraction, skipping batch submission")
		return stats, nil
	}

	// Initialize components for content extraction (no LLM needed here)
	newsSearcher, err := search.NewNewsSearcherFromEnv()
	if err != nil {
		return stats, fmt.Errorf("failed to initialize news search: %w", err)
	}

	rawExtractor, err := extractor.NewExtractorFromType("webcrawler")
	if err != nil {
		return stats, fmt.Errorf("failed to initialize webcrawler: %w", err)
	}
	batchExtractor := core.NewBatchExtractorFromEnv(rawExtractor)

	// News provider — only needs search + content extraction (no FactExtractor/Embedder)
	newsProvider := &news.Provider{
		NewsSearch:       newsSearcher,
		ContentExtractor: batchExtractor,
		Storer:           storer,
	}

	newsOpts := news.Options{
		MaxParse:     5,
		Location:     "France",
		RevisitHours: 24 * 365 * 10,
	}

	// Collect content from all target persons
	var allItems []core.BatchContentItem
	for i, entry := range targets {
		log.Info().Int("progress", i+1).Int("total", len(targets)).Str("person", entry.Name).Msg("Collecting news content")

		targetType := news.TargetPerson
		if entry.EntityType == "party" {
			targetType = news.TargetParty
		}
		target := news.SearchTarget{
			Name:     entry.Name,
			Type:     targetType,
			PersonID: &entry.PersonID,
		}

		items, result, err := newsProvider.CollectContent(ctx, target, newsOpts)
		if err != nil {
			log.Error().Err(err).Str("person", entry.Name).Msg("Failed to collect content")
			stats.CollectErrors = append(stats.CollectErrors, entry.Name+": "+err.Error())
			continue
		}

		stats.TargetsProcessed++
		if result != nil {
			stats.ArticlesParsed += result.ArticlesParsed
			stats.ScrapeErrors += len(result.Errors)

			log.Info().
				Str("person", entry.Name).
				Int("articles_found", result.ArticlesFound).
				Int("articles_parsed", result.ArticlesParsed).
				Int("content_items", len(items)).
				Msg("Content collection completed")
		}

		// Convert news ContentItems to batch BatchContentItems
		for _, item := range items {
			allItems = append(allItems, core.BatchContentItem{
				SourceURL:   item.SourceURL,
				Content:     item.Content,
				TargetName:  item.TargetName,
				TargetType:  item.TargetType,
				PublishedAt: item.PublishedAt,
			})
		}
	}

	// Also collect Twitter content for batch submission (filtered to same target set)
	twitterItems, twitterErr := collectTwitterContentForBatch(ctx, storer, targetIDs)
	if twitterErr != nil {
		log.Warn().Err(twitterErr).Msg("Twitter content collection for batch failed")
		stats.CollectErrors = append(stats.CollectErrors, "twitter: "+twitterErr.Error())
	} else if len(twitterItems) > 0 {
		allItems = append(allItems, twitterItems...)
		log.Info().Int("twitter_items", len(twitterItems)).Msg("Added Twitter content to batch")
	}

	stats.ContentItems = len(allItems)

	if len(allItems) == 0 {
		log.Info().Msg("No content collected, skipping batch submission")
		return stats, nil
	}

	// Initialize batch provider and submit
	provider, err := newBatchProvider()
	if err != nil {
		return stats, fmt.Errorf("failed to create batch provider: %w", err)
	}

	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return stats, fmt.Errorf("failed to create embedder: %w", err)
	}

	stanceMgr, err := core.NewStanceManagerFromEnv(storage.NewStanceStorerAdapter(storer))
	if err != nil {
		return stats, fmt.Errorf("stance manager required: %w", err)
	}
	inserter := core.NewFactInserter(storer, stanceMgr)
	adapter := newBatchStorerAdapter(storer)
	orchestrator := core.NewBatchJobOrchestrator(provider, adapter, embedder, inserter, categorizer.ParseFactJSON)

	batchID, err := orchestrator.PrepareAndSubmit(ctx, allItems, map[string]any{
		"source":    "daily-pipeline",
		"submitted": time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return stats, fmt.Errorf("batch submission failed: %w", err)
	}

	log.Info().
		Int64("batch_id", batchID).
		Int("content_items", len(allItems)).
		Msg("Batch submitted — poller will process results")

	return stats, nil
}

// collectTwitterContentForBatch fetches tweets for active handles whose person is
// in targetIDs and returns BatchContentItems for batch LLM submission. No LLM
// calls are made.
func collectTwitterContentForBatch(ctx context.Context, storer *storage.PostgresStorer, targetIDs map[int64]bool) ([]core.BatchContentItem, error) {
	handles, err := storer.GetActiveTwitterHandles(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get active Twitter handles: %w", err)
	}
	if len(handles) == 0 {
		log.Info().Msg("No active Twitter handles, skipping Twitter batch collection")
		return nil, nil
	}

	fetcher, err := twitter.NewTwitterAPIIOFetcherFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("Twitter API key not configured, skipping Twitter batch collection")
		return nil, nil
	}

	provider := &twitter.Provider{
		Fetcher: fetcher,
		Storer:  storer,
	}

	opts := twitter.Options{
		MaxTweets: 50,
		BatchSize: 20,
	}

	var allItems []core.BatchContentItem
	skipped := 0
	for _, handle := range handles {
		if !targetIDs[handle.PersonID] {
			skipped++
			continue
		}
		items, err := provider.CollectContent(ctx, handle, opts)
		if err != nil {
			log.Error().Err(err).Str("handle", handle.Handle).Msg("Failed to collect Twitter content for batch")
			continue
		}
		allItems = append(allItems, items...)
	}

	if skipped > 0 {
		log.Info().Int("skipped_handles", skipped).Msg("Twitter batch collection: skipped handles outside target set")
	}

	return allItems, nil
}

// runFrpoliticsStep executes the frpolitics step of the daily pipeline:
// votes + news for French deputies. Uses the same defaults as the legacy
// standalone scheduler (now removed).
func runFrpoliticsStep(ctx context.Context, storer *storage.PostgresStorer) error {
	start := time.Now()
	log.Info().Msg("Frpolitics step started")

	opts := frpolitics.Options{
		Mode:             "both",
		MaxLaws:          10,
		PartyThreshold:   0.8,
		NewsLimit:        5,
		NewsLocation:     "France",
		ContentExtractor: "webcrawler",
	}

	err := frpolitics.Run(ctx, storer, opts)
	log.Info().Dur("elapsed", time.Since(start)).Err(err).Msg("Frpolitics step finished")
	return err
}

// runStancesMergeStep executes the stance merge sweep step of the daily
// pipeline: scans every person's stances for centroid-similar candidates,
// LLM-vets each candidate cluster, applies merges, and re-detects
// contradictions across the previously-fragmented facts. Uses the CLI's
// default thresholds (Phase-A 0.65 sim recall + Phase-B LLM vetting).
func runStancesMergeStep(ctx context.Context, storer *storage.PostgresStorer) error {
	start := time.Now()
	log.Info().Msg("Stance merge sweep started")

	adapter := storage.NewStanceStorerAdapter(storer)
	stanceManager, err := core.NewStanceManagerFromEnv(adapter)
	if err != nil {
		return fmt.Errorf("stance manager required for merge sweep: %w", err)
	}

	// SQL pre-filter: only consider persons with >= 2 stances (the rest can't
	// possibly merge). Cuts a 3849-person DB-query loop down to ~100-300.
	persons, err := storer.GetPersonsWithMultipleStances(ctx)
	if err != nil {
		return fmt.Errorf("listing merge-candidate persons: %w", err)
	}

	opts := defaultMergeOptions

	var personsProcessed, mergedAway, contradictionsFound, analyzeFailures int64
	for _, p := range persons {
		ps, perr := mergeStancesForPersonViaLLM(ctx, storer, stanceManager, p, opts)
		if perr != nil {
			log.Warn().Err(perr).Str("person", p.Name).Msg("Merge sweep: per-person failure (continuing)")
			continue
		}
		personsProcessed++
		mergedAway += ps.stancesMerged
		contradictionsFound += ps.postMergeContradictions
		analyzeFailures += ps.postMergeAnalyzeFailures
	}

	log.Info().
		Dur("elapsed", time.Since(start)).
		Int64("persons_processed", personsProcessed).
		Int64("stances_merged", mergedAway).
		Int64("post_merge_contradictions", contradictionsFound).
		Int64("post_merge_analyze_failures", analyzeFailures).
		Msg("Stance merge sweep finished")
	return nil
}

// runTwitterCollection fetches tweets for active Twitter handles whose person is
// in targetIDs and extracts facts. Gracefully skips if no handles exist or if
// the Twitter API key is not configured.
func runTwitterCollection(ctx context.Context, storer *storage.PostgresStorer, targetIDs map[int64]bool) error {
	handles, err := storer.GetActiveTwitterHandles(ctx)
	if err != nil {
		return fmt.Errorf("failed to get active Twitter handles: %w", err)
	}
	if len(handles) == 0 {
		log.Info().Msg("No active Twitter handles, skipping Twitter collection")
		return nil
	}

	fetcher, err := twitter.NewTwitterAPIIOFetcherFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("Twitter API key not configured, skipping Twitter collection")
		return nil
	}

	factExtractor, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize fact extractor for Twitter: %w", err)
	}

	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("failed to initialize embedder for Twitter: %w", err)
	}

	provider := &twitter.Provider{
		Fetcher:       fetcher,
		FactExtractor: factExtractor,
		Embedder:      embedder,
		Storer:        storer,
	}

	opts := twitter.Options{
		MaxTweets: 50,
		BatchSize: 20,
	}

	var (
		firstErr     error
		totalTweets  int
		totalFacts   int
		handlesOK    int
		handlesUsed  int
		handlesSkipd int
	)

	for _, handle := range handles {
		if !targetIDs[handle.PersonID] {
			handlesSkipd++
			continue
		}
		handlesUsed++
		result, err := provider.CollectFacts(ctx, handle, opts)
		if err != nil {
			log.Error().Err(err).Str("handle", handle.Handle).Msg("Twitter collection failed for handle")
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		handlesOK++
		if result != nil {
			totalTweets += result.TweetsFetched
			totalFacts += result.FactsExtracted
		}
	}

	log.Info().
		Int("handles_total", len(handles)).
		Int("handles_used", handlesUsed).
		Int("handles_skipped", handlesSkipd).
		Int("handles_ok", handlesOK).
		Int("tweets_fetched", totalTweets).
		Int("facts_extracted", totalFacts).
		Msg("Twitter collection completed")

	return firstErr
}

// runTwitterDiscovery auto-discovers Twitter handles for persons who don't have one yet.
// Gracefully skips if web search or LLM is not configured.
func runTwitterDiscovery(ctx context.Context, storer *storage.PostgresStorer) error {
	return executeTwitterDiscovery(ctx, storer, TwitterDiscoveryOpts{})
}

// runPhotosBackfill searches and uploads profile photos for every person/party
// that doesn't have one yet. Gracefully no-ops if image search or R2 is not
// configured (treated as a degraded but non-fatal state for the daily pipeline).
func runPhotosBackfill(ctx context.Context, storer *storage.PostgresStorer) error {
	persons, err := storer.GetPersonsWithoutImage(ctx)
	if err != nil {
		return fmt.Errorf("failed to get persons without image: %w", err)
	}
	if len(persons) == 0 {
		log.Info().Msg("All persons have photos, skipping photos backfill")
		return nil
	}

	imageSearcher, err := search.NewImageSearcherFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("Image search not configured, skipping photos backfill")
		return nil
	}

	r2, err := objectstore.NewR2ClientFromEnv()
	if err != nil {
		log.Warn().Err(err).Msg("R2 not configured, skipping photos backfill")
		return nil
	}

	provider := &photos.Provider{
		ImageSearch: imageSearcher,
		R2:          r2,
		Storer:      storer,
	}

	opts := photos.Options{
		MaxResults: 10,
		MinWidth:   200,
		MinHeight:  200,
	}

	log.Info().Int("persons", len(persons)).Msg("Photos backfill started")

	uploaded, skipped, errs := 0, 0, 0
	for i, person := range persons {
		log.Info().
			Int("progress", i+1).
			Int("total", len(persons)).
			Str("person", person.Name).
			Msg("Photos backfill: processing person")

		result := provider.SearchAndUpload(ctx, person, opts)
		if result.Error != nil {
			log.Error().Err(result.Error).Str("person", person.Name).Msg("Photo search failed")
			errs++
			continue
		}
		if result.Skipped {
			log.Info().Str("person", person.Name).Str("reason", result.SkipReason).Msg("Photos backfill: skipped")
			skipped++
			continue
		}
		if result.Uploaded {
			log.Info().Str("person", person.Name).Str("url", result.ImageURL).Msg("Photo uploaded")
			uploaded++
		}
	}

	log.Info().
		Int("total", len(persons)).
		Int("uploaded", uploaded).
		Int("skipped", skipped).
		Int("errors", errs).
		Msg("Photos backfill completed")

	jobMetrics := []storage.JobMetric{
		{JobType: "photos-backfill", Target: "", Metric: "uploaded", Value: float64(uploaded)},
		{JobType: "photos-backfill", Target: "", Metric: "skipped", Value: float64(skipped)},
		{JobType: "photos-backfill", Target: "", Metric: "errors", Value: float64(errs)},
	}
	if err := storer.RecordJobMetrics(ctx, jobMetrics); err != nil {
		log.Error().Err(err).Msg("Failed to record photos backfill job metrics")
	}

	return nil
}
