package cmd

import (
	"context"
	"errors"
	"fmt"
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
//  1. news fact extraction      — queue-scoped (search_queue)
//  2. twitter fact extraction   — queue-scoped (discovery + collection)
//  3. frpolitics                — French Assemblée votes only (own scope, no news)
//  4. contradiction detection   — over all persons with new pairs
//  5. photo backfill            — persons + parties missing an image
func runDailyPipeline(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier) error {
	start := time.Now()
	log.Info().Msg("Daily pipeline started")

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

	// Step 1: News fact extraction — synchronous via the OPEN_AI_API LLM
	// (lite-llm → local GPU). Searches news per target, scrapes, extracts and
	// inserts facts inline.
	stats, newsErr := runNewsCollection(ctx, storer, targets)
	if newsErr != nil {
		log.Error().Err(newsErr).Msg("Daily pipeline: news fact extraction failed")
	}

	// Step 2: Twitter — discovery + fact collection, both queue-scoped. Scoping
	// to the queue eliminates the long-tail false-handle problem we saw with
	// @ambrusfrance → Alexey Meshkov and cuts the Serper bill on every cron.
	discoverErr := runTwitterDiscovery(ctx, storer, targetIDs)
	if discoverErr != nil {
		log.Error().Err(discoverErr).Msg("Daily pipeline: Twitter discovery failed")
	}

	twitterErr := runTwitterCollection(ctx, storer, targetIDs)
	if twitterErr != nil {
		log.Error().Err(twitterErr).Msg("Daily pipeline: Twitter collection failed")
	}

	// Step 3: frpolitics — French Assemblée vote facts only. News is handled by
	// step 1 (queue-scoped); frpolitics no longer sweeps news so it can't pull in
	// the whole person table via its full-DB news fallback.
	fpErr := runFrpoliticsStep(ctx, storer)
	if fpErr != nil {
		log.Error().Err(fpErr).Msg("Daily pipeline: frpolitics failed")
	}

	// Step 3b: Translation repair. All fact-creating steps are done; fill any
	// non-English facts whose extraction-time English translation came back empty
	// (the model occasionally drops the field). Best-effort — failures are logged
	// and retried next run, never aborting the pipeline.
	if filled, failed, trErr := repairMissingTranslations(ctx, storer, 5000); trErr != nil {
		log.Error().Err(trErr).Msg("Daily pipeline: translation repair failed")
	} else if filled > 0 || failed > 0 {
		log.Info().Int("filled", filled).Int("failed", failed).Msg("Daily pipeline: translation repair done")
	}

	// Step 4: Stance merge sweep. Looks for stances that should be merged per
	// LLM-vetted centroid clustering, applies the merges, and re-detects
	// contradictions across previously-separated facts. Catches the cross-stance
	// contradictions our per-fact assignTopic missed by mis-grouping.
	mergeErr := runStancesMergeStep(ctx, storer)
	if mergeErr != nil {
		log.Error().Err(mergeErr).Msg("Daily pipeline: stance merge sweep failed")
	}

	// Step 5: Contradiction detection. Runs synchronously over unchecked pairs
	// across all persons via the OPEN_AI_API LLM, with the inline verification
	// pass gating each YES before it is recorded.
	contraErr := executeContradictionDetection(ctx, storer, ContradictionDetectOpts{})
	if contraErr != nil {
		log.Error().Err(contraErr).Msg("Daily pipeline: contradiction detection failed")
	}

	// Step 5b: Repoint contradictions off any facts the contradiction-pass dedup
	// just marked as duplicates, so the redundant rows stop inflating scores.
	// Best-effort — failures are logged, never aborting the pipeline.
	if processed, repointed, dropped, rErr := repointDuplicates(ctx, storer, true); rErr != nil {
		log.Error().Err(rErr).Msg("Daily pipeline: repoint-duplicates failed")
	} else if processed > 0 {
		log.Info().Int("duplicates", processed).Int("repointed", repointed).Int("dropped", dropped).
			Msg("Daily pipeline: repointed contradictions off duplicate facts")
	}

	// Step 5c: Regroup contradictions into N-ary groups (one base fact + N
	// contradicting facts) and (re)generate each group's merged summary. Runs
	// after detection + duplicate-repointing so it sees the final pairwise set.
	// Best-effort — failures are logged, never aborting the pipeline.
	if regroupErr := runContradictionRegroupStep(ctx, storer); regroupErr != nil {
		log.Error().Err(regroupErr).Msg("Daily pipeline: contradiction regroup failed")
	}

	// Step 6: Photos backfill (persons + parties missing an image).
	photosErr := runPhotosBackfill(ctx, storer)
	if photosErr != nil {
		log.Error().Err(photosErr).Msg("Daily pipeline: photos backfill failed")
	}

	// Step 7: Contradiction auditor (5-day cadence). Re-judges previously
	// recorded contradictions and flags false positives via the
	// `contradiction_audits_total{verdict="remove"}` Prometheus counter.
	// READ-ONLY today — it identifies but does not delete; act on its findings
	// with `dm contradictions check --apply` once Grafana shows the pattern
	// is reliable.
	auditResult, auditErr := runContradictionAuditIfDue(ctx, storer, 5*24*time.Hour)
	if auditErr != nil {
		log.Error().Err(auditErr).Msg("Daily pipeline: contradiction audit failed")
	}

	elapsed := time.Since(start)

	// Classify the run once; both the Telegram message and the pipeline_runs row
	// derive their status from these flags.
	stepErrPairs := []struct {
		step string
		err  error
	}{
		{"targets", targetsErr}, {"news", newsErr}, {"twitter-discover", discoverErr},
		{"twitter", twitterErr}, {"frpolitics", fpErr}, {"stance-merge", mergeErr},
		{"contradictions", contraErr}, {"photos", photosErr}, {"contradiction-audit", auditErr},
	}
	var stepErrs []error
	var errParts []string
	for _, p := range stepErrPairs {
		if p.err != nil {
			stepErrs = append(stepErrs, p.err)
			errParts = append(errParts, p.step+": "+p.err.Error())
		}
	}
	hasErrors := len(stepErrs) > 0
	hasWarnings := stats != nil && (stats.ScrapeErrors > 0 || len(stats.CollectErrors) > 0)
	noContent := stats != nil && stats.TargetsProcessed > 0 && stats.ContentItems == 0

	status := "success"
	switch {
	case hasErrors:
		status = "error"
	case noContent:
		status = "no_content"
	case hasWarnings:
		status = "warning"
	}

	// Notify via Telegram
	if notifier != nil {
		var msg string
		switch status {
		case "error":
			msg = fmt.Sprintf("❌ <b>Daily pipeline errors</b> (%s)\n%s",
				elapsed.Round(time.Second), strings.Join(errParts, "\n"))
		case "no_content":
			msg = fmt.Sprintf("⚠️ <b>Daily pipeline completed with no content</b> (%s)",
				elapsed.Round(time.Second))
		case "warning":
			msg = fmt.Sprintf("⚠️ <b>Daily pipeline completed with warnings</b> (%s)",
				elapsed.Round(time.Second))
		default:
			msg = fmt.Sprintf("✅ <b>Daily pipeline completed</b> (%s)",
				elapsed.Round(time.Second))
		}

		if stats != nil {
			msg += "\n" + stats.summary()
		}
		if auditResult != nil && auditResult.Total > 0 {
			msg += fmt.Sprintf("\n🔍 audit: %d checked, %d evicted (2-of-2 FP), %d kept (unstable)",
				auditResult.Total, auditResult.Deleted, auditResult.Unstable)
		}

		if err := notifier.Send(ctx, msg); err != nil {
			log.Error().Err(err).Msg("Failed to send pipeline Telegram notification")
		}
	}

	// Persist a durable per-run summary for Grafana (Postgres datasource). The
	// cron process is ephemeral so Prometheus cannot scrape it; this row is the
	// source of truth for pipeline-run history. Best-effort — a write failure is
	// logged but never fails the run.
	finishedAt := time.Now()
	summary := storage.PipelineRunSummary{
		StartedAt:    start,
		FinishedAt:   finishedAt,
		Status:       status,
		StepErrors:   len(stepErrs),
		ErrorSummary: strings.Join(errParts, "\n"),
	}
	if stats != nil {
		summary.Targets = stats.TargetsProcessed
		summary.NewsArticles = stats.ArticlesParsed
		summary.ScrapeErrors = stats.ScrapeErrors
	}
	if auditResult != nil {
		summary.AuditChecked = auditResult.Total
		summary.AuditFlagged = auditResult.Remove
	}
	if err := storer.RecordPipelineRun(ctx, summary); err != nil {
		log.Error().Err(err).Msg("Failed to record pipeline_runs summary")
	}

	log.Info().
		Dur("elapsed", elapsed).
		Int("step_errors", len(stepErrs)).
		Msg("Daily pipeline finished")

	return errors.Join(stepErrs...)
}

// dailyTargets returns the persons that fact-extraction steps (news, twitter)
// iterate over: the search_queue contents (the "main persons queue").
// Photos + frpolitics + contradictions ignore this filter — they have their own scope.
func dailyTargets(ctx context.Context, storer *storage.PostgresStorer) ([]storage.QueueEntry, error) {
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
	msg := fmt.Sprintf("targets=%d articles=%d facts=%d scrape_errors=%d",
		s.TargetsProcessed, s.ArticlesParsed, s.ContentItems, s.ScrapeErrors)
	if len(s.CollectErrors) > 0 {
		msg += fmt.Sprintf("\n⚠️ Collection errors (%d):", len(s.CollectErrors))
		for _, e := range s.CollectErrors {
			msg += "\n  • " + e
		}
	}
	return msg
}

// runNewsCollection searches news for each target person/party and extracts +
// stores facts synchronously via the OPEN_AI_API LLM (lite-llm → local GPU).
// Replaces the former batch-submission path. Returns aggregate stats and the
// first fatal setup error (per-target failures are collected, not returned).
func runNewsCollection(ctx context.Context, storer *storage.PostgresStorer, targets []storage.QueueEntry) (*pipelineStats, error) {
	stats := &pipelineStats{}

	if len(targets) == 0 {
		log.Info().Msg("No target persons for news fact extraction, skipping")
		return stats, nil
	}

	newsSearcher, err := search.NewNewsSearcherFromEnv()
	if err != nil {
		return stats, fmt.Errorf("failed to initialize news search: %w", err)
	}
	rawExtractor, err := extractor.NewExtractorFromType("auto")
	if err != nil {
		return stats, fmt.Errorf("failed to initialize content extractor: %w", err)
	}
	contentExtractor := core.NewBatchExtractorFromEnv(rawExtractor)
	factExtractor, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		return stats, fmt.Errorf("failed to initialize fact extractor: %w", err)
	}
	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return stats, fmt.Errorf("failed to initialize embedder: %w", err)
	}
	stanceMgr, err := core.NewStanceManagerFromEnv(storage.NewStanceStorerAdapter(storer))
	if err != nil {
		return stats, fmt.Errorf("stance manager required: %w", err)
	}

	newsProvider := &news.Provider{
		NewsSearch:       newsSearcher,
		ContentExtractor: contentExtractor,
		FactExtractor:    factExtractor,
		Embedder:         embedder,
		Storer:           storer,
		StanceManager:    stanceMgr,
	}

	newsOpts := news.Options{
		MaxParse:     5,
		Location:     "France",
		RevisitHours: 24 * 365 * 10,
	}

	for i, entry := range targets {
		log.Info().Int("progress", i+1).Int("total", len(targets)).Str("person", entry.Name).Msg("News fact extraction")

		targetType := news.TargetPerson
		if entry.EntityType == "party" {
			targetType = news.TargetParty
		}
		target := news.SearchTarget{
			Name:     entry.Name,
			Type:     targetType,
			PersonID: &entry.PersonID,
		}

		result, err := newsProvider.CollectFacts(ctx, target, newsOpts)
		if err != nil {
			log.Error().Err(err).Str("person", entry.Name).Msg("News fact extraction failed")
			stats.CollectErrors = append(stats.CollectErrors, entry.Name+": "+err.Error())
			continue
		}

		stats.TargetsProcessed++
		if result != nil {
			stats.ArticlesParsed += result.ArticlesParsed
			stats.ScrapeErrors += len(result.Errors)
			stats.ContentItems += result.FactsExtracted

			log.Info().
				Str("person", entry.Name).
				Int("articles_found", result.ArticlesFound).
				Int("articles_parsed", result.ArticlesParsed).
				Int("facts_extracted", result.FactsExtracted).
				Msg("News fact extraction completed")
		}
	}

	return stats, nil
}

// runFrpoliticsStep executes the frpolitics step of the daily pipeline:
// votes + news for French deputies. Uses the same defaults as the legacy
// standalone scheduler (now removed).
func runFrpoliticsStep(ctx context.Context, storer *storage.PostgresStorer) error {
	start := time.Now()
	log.Info().Msg("Frpolitics step started")

	// Vote facts only. News for tracked persons is handled by the queue-scoped
	// news step (step 1); running frpolitics in "both"/"news" mode here would
	// fall through to its full-DB GetAllPersons news sweep and pull in the whole
	// person table (foreign parties, junk duplicates) — the exact bug we removed.
	opts := frpolitics.Options{
		Mode:           "votes",
		MaxLaws:        10,
		PartyThreshold: 0.8,
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

// runTwitterDiscovery auto-discovers Twitter handles for persons in the search
// queue who don't have one yet. Queue-scoped on purpose — we don't want to
// auto-search Twitter for the long tail of secondary persons mentioned in
// articles (3 000+ entries today, vs ~7 in the queue), and historically that
// untargeted search produced false positives like @ambrusfrance →
// Alexey Meshkov. Gracefully skips if web search or LLM is not configured.
func runTwitterDiscovery(ctx context.Context, storer *storage.PostgresStorer, targetIDs map[int64]bool) error {
	ids := make([]int64, 0, len(targetIDs))
	for id := range targetIDs {
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		log.Info().Msg("Twitter discovery: no queue targets, skipping")
		return nil
	}
	return executeTwitterDiscovery(ctx, storer, TwitterDiscoveryOpts{TargetIDs: ids})
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

// runContradictionAuditIfDue runs the contradiction auditor at most once per
// `cadence` interval. The cadence is enforced via the job_metrics timestamp
// for job_type="contradiction-audit" — last-run is read at the start, and
// executeContradictionAudit (with RecordMetric=true) writes a new row at the end.
// Returns the result (nil if skipped) so the caller can include audit stats in
// the daily Telegram message.
func runContradictionAuditIfDue(ctx context.Context, storer *storage.PostgresStorer, cadence time.Duration) (*ContradictionAuditResult, error) {
	last, err := storer.GetLatestJobRunTime(ctx, "contradiction-audit")
	if err != nil {
		// Reading the timestamp failed — log and skip (don't keep retrying every
		// daily tick; the next successful read will get the audit going again).
		log.Error().Err(err).Msg("Contradiction audit: failed to read last-run timestamp, skipping this cycle")
		return nil, nil
	}
	if last != nil && time.Since(*last) < cadence {
		log.Info().
			Time("last_run", *last).
			Dur("cadence", cadence).
			Dur("age", time.Since(*last)).
			Msg("Contradiction audit: not due yet, skipping")
		return nil, nil
	}

	log.Info().Dur("cadence", cadence).Msg("Contradiction audit: due, starting pass")
	return executeContradictionAudit(ctx, storer, ContradictionAuditOpts{
		Output:       nil, // silent — pipeline mode, no per-row stdout
		Format:       "",
		Scope:        "all persons (periodic)",
		RecordMetric: true,
		// Auto-evict: a verdict=remove confirmed by a 2nd auditor pass (2-of-2) is
		// deleted via DismissContradictionAsFalsePositive (negative pair check +
		// score recompute), so false positives never accumulate between runs.
		Apply: true,
	})
}
