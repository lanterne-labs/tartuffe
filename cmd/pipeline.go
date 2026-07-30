package cmd

import (
	"context"
	"digital-mirror/backend/llm"
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

// runPipeline executes one configured pipeline run and returns an aggregated
// error if any step failed. There is exactly one pipeline implementation: the
// nightly run is `runPipeline` with the zero PipelineConfig (every step, the
// search queue, no date window), and an ad-hoc job is the same code with a
// narrower config. jobName labels the run in logs, Telegram and pipeline_runs.
//
// Steps in order (each skippable via PipelineConfig.Steps):
//  1. news fact extraction      — per target, honours the date window
//  2. twitter fact extraction   — per target (discovery + collection), honours the date window
//  3. frpolitics                — French Assemblée votes; neither entity- nor date-addressable,
//     so it is skipped whenever the job is scoped
//  4. stance merge sweep        — target-scoped when the job is
//  5. contradiction detection   — target-scoped when the job is (+ repoint + regroup)
//  6. photo backfill            — target-scoped when the job is
//  7. contradiction audit       — cadence-gated globally; on a scoped job it audits the targets now
//  8. translation phase         — always global, capped per run
//
// runID is returned alongside the error so the caller (the queue worker) can
// link the job to its pipeline_runs summary row.
func runPipeline(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier, jobName string, cfg PipelineConfig) (int64, error) {
	start := time.Now()
	if jobName == "" {
		jobName = cfg.DefaultName()
	}
	log.Info().
		Str("job", jobName).
		Strs("steps", cfg.enabledSteps()).
		Strs("targets", cfg.Targets).
		Str("window", cfg.windowLabel()).
		Msg("Pipeline started")

	// Record the run's existence BEFORE doing anything, so a killed or hung run
	// leaves a 'running' row instead of vanishing (mirror-bp0). Best-effort:
	// runID 0 makes RecordPipelineRun fall back to a full INSERT at the end.
	runID, runIDErr := storer.StartPipelineRun(ctx, start, jobName)
	if runIDErr != nil {
		log.Error().Err(runIDErr).Msg("Pipeline: failed to record run start; summary will be inserted at end")
	}

	// Probe the primary LLM endpoint once. If the home box is unreachable, arm
	// the circuit breaker now so EVERY call this run goes straight to the
	// fallback instead of burning the full retry backoff per call — an outage
	// used to turn 3-hour runs into 10-hour ones (mirror-s8t).
	if !llm.ProbePrimary(ctx) {
		log.Warn().Msg("Pipeline: primary LLM endpoint unreachable — routing the entire run to the fallback")
	}

	// Resolve target persons for fact-extraction steps (news, twitter): the
	// job's explicit targets, or the search queue when it has none.
	targets, targetsErr := pipelineTargets(ctx, storer, cfg)
	if targetsErr != nil {
		log.Error().Err(targetsErr).Msg("Pipeline: failed to resolve target persons")
	}
	targetIDs := make(map[int64]bool, len(targets))
	for _, t := range targets {
		targetIDs[t.PersonID] = true
	}
	log.Info().Int("targets", len(targets)).Msg("Pipeline: target persons resolved")

	// A scoped job whose targets did not resolve must abort: with an empty scope
	// the entity-wide steps would fall back to their global sweep, so a typo'd
	// target would silently reprocess the entire database.
	if cfg.scoped() {
		if targetsErr == nil && len(targets) == 0 {
			targetsErr = fmt.Errorf("job targets %v resolved to no entities", cfg.Targets)
		}
		if targetsErr != nil {
			log.Error().Err(targetsErr).Msg("Pipeline: aborting scoped run — refusing to fall back to a global sweep")
			return runID, finalizePipelineRun(ctx, storer, notifier, jobName, start, runID, nil, nil,
				[]stepOutcome{{"targets", targetsErr}})
		}
	}

	// scopeIDs restricts the entity-wide steps (stance merge, contradictions,
	// photos, audit) to the job's targets. nil = every entity, the nightly
	// behaviour.
	var scopeIDs []int64
	if cfg.scoped() {
		for _, t := range targets {
			scopeIDs = append(scopeIDs, t.PersonID)
		}
	}

	// Step 1: News fact extraction — synchronous via the OPEN_AI_API LLM
	// (lite-llm → local GPU). Searches news per target, scrapes, extracts and
	// inserts facts inline. Honours the job's date window.
	var stats *pipelineStats
	var newsErr error
	if cfg.enabled(StepNews) {
		stats, newsErr = runNewsCollection(ctx, storer, targets, cfg)
		if newsErr != nil {
			log.Error().Err(newsErr).Msg("Pipeline: news fact extraction failed")
		}
	} else {
		skipStep(StepNews, "not enabled by the job config")
	}

	// Step 2: Twitter — discovery + fact collection, both target-scoped. Scoping
	// eliminates the long-tail false-handle problem we saw with
	// @ambrusfrance → Alexey Meshkov and cuts the Serper bill on every cron.
	var discoverErr, twitterErr error
	if cfg.enabled(StepTwitter) {
		discoverErr = runTwitterDiscovery(ctx, storer, targetIDs)
		if discoverErr != nil {
			log.Error().Err(discoverErr).Msg("Pipeline: Twitter discovery failed")
		}

		twitterErr = runTwitterCollection(ctx, storer, targetIDs, cfg)
		if twitterErr != nil {
			log.Error().Err(twitterErr).Msg("Pipeline: Twitter collection failed")
		}
	} else {
		skipStep(StepTwitter, "not enabled by the job config")
	}

	// Step 3: frpolitics — French Assemblée vote facts only. News is handled by
	// step 1 (target-scoped); frpolitics no longer sweeps news so it can't pull in
	// the whole person table via its full-DB news fallback. The AN APIs expose
	// neither a person filter nor a publication-date filter, so a scoped job skips
	// it instead of silently doing a full global sweep.
	var fpErr error
	switch {
	case !cfg.enabled(StepFrpolitics):
		skipStep(StepFrpolitics, "not enabled by the job config")
	case cfg.hasDateWindow() || cfg.scoped():
		skipStep(StepFrpolitics, "Assemblée vote scraping supports neither a date window nor a target filter")
	default:
		fpErr = runFrpoliticsStep(ctx, storer)
		if fpErr != nil {
			log.Error().Err(fpErr).Msg("Pipeline: frpolitics failed")
		}
	}

	// (Former Step 3b "translation repair" removed: the final translation phase
	// (Step 8) now fills every language for facts/stances/contradictions, so the
	// mid-pipeline EN-only fact repair is redundant.)

	// Step 4: Stance merge sweep. Looks for stances that should be merged per
	// LLM-vetted centroid clustering, applies the merges, and re-detects
	// contradictions across previously-separated facts. Catches the cross-stance
	// contradictions our per-fact assignTopic missed by mis-grouping.
	var mergeErr error
	if cfg.enabled(StepStanceMerge) {
		mergeErr = runStancesMergeStep(ctx, storer, scopeIDs)
		if mergeErr != nil {
			log.Error().Err(mergeErr).Msg("Pipeline: stance merge sweep failed")
		}
	} else {
		skipStep(StepStanceMerge, "not enabled by the job config")
	}

	// Step 5: Contradiction detection. Runs synchronously over unchecked pairs
	// via the OPEN_AI_API LLM, with the inline verification pass gating each YES
	// before it is recorded. Followed by duplicate repointing and regrouping, so
	// the facts this run added land in their final N-ary groups.
	var contraErr error
	if cfg.enabled(StepContradictions) {
		contraErr = runContradictionDetectionStep(ctx, storer, cfg)
		if contraErr != nil {
			log.Error().Err(contraErr).Msg("Pipeline: contradiction detection failed")
		}

		// Step 5b: Repoint contradictions off any facts the contradiction-pass dedup
		// just marked as duplicates, so the redundant rows stop inflating scores.
		// Best-effort — failures are logged, never aborting the pipeline.
		if processed, repointed, dropped, rErr := repointDuplicates(ctx, storer, true); rErr != nil {
			log.Error().Err(rErr).Msg("Pipeline: repoint-duplicates failed")
		} else if processed > 0 {
			log.Info().Int("duplicates", processed).Int("repointed", repointed).Int("dropped", dropped).
				Msg("Pipeline: repointed contradictions off duplicate facts")
		}

		// Step 5c: Regroup contradictions into N-ary groups (one base fact + N
		// contradicting facts) and (re)generate each group's merged summary. Runs
		// after detection + duplicate-repointing so it sees the final pairwise set.
		// Best-effort — failures are logged, never aborting the pipeline.
		if regroupErr := runContradictionRegroupStep(ctx, storer, scopeIDs); regroupErr != nil {
			log.Error().Err(regroupErr).Msg("Pipeline: contradiction regroup failed")
		}
	} else {
		skipStep(StepContradictions, "not enabled by the job config")
	}

	// Step 6: Photos backfill (persons + parties missing an image).
	var photosErr error
	if cfg.enabled(StepPhotos) {
		photosErr = runPhotosBackfill(ctx, storer, targetIDs, cfg.scoped())
		if photosErr != nil {
			log.Error().Err(photosErr).Msg("Pipeline: photos backfill failed")
		}
	} else {
		skipStep(StepPhotos, "not enabled by the job config")
	}

	// Step 7: Contradiction auditor (5-day cadence). Re-judges previously
	// recorded contradictions and AUTO-EVICTS confirmed false positives
	// (Apply:true). A row is deleted only when THREE signals agree: the audit
	// verdict is remove, a confirming second audit pass also says remove, and
	// the verifier (refute pass — a different, reversal-aware judge) also
	// drops it (cross-judge veto; see applyAuditRemoval). Any disagreement or
	// error keeps the row and counts it as unstable. Verdicts are also exposed
	// via the `contradiction_audits_total` Prometheus counter.
	//
	// A target-scoped job audits ITS targets immediately and does not touch the
	// global cadence: the 5-day gate (and the job_metrics stamp behind it) belongs
	// to the nightly sweep, and an ad-hoc backfill must not postpone it.
	var auditResult *ContradictionAuditResult
	var auditErr error
	if cfg.enabled(StepAudit) {
		if cfg.scoped() {
			auditResult, auditErr = runScopedContradictionAudit(ctx, storer, targets)
		} else {
			auditResult, auditErr = runContradictionAuditIfDue(ctx, storer, 5*24*time.Hour)
		}
		if auditErr != nil {
			log.Error().Err(auditErr).Msg("Pipeline: contradiction audit failed")
		}
	} else {
		skipStep(StepAudit, "not enabled by the job config")
	}

	// Step 8 (final): Translation phase — decoupled from generation. Fills missing
	// or stale translations for facts, stances and contradictions into the generic
	// translations table for every target language. Runs LAST so generation never
	// blocks on it. Global by design (it drains whatever backlog exists, capped
	// per run). Best-effort — logged, never aborts the pipeline.
	var trErr error
	if cfg.enabled(StepTranslate) {
		trErr = runTranslateStep(ctx, storer)
		if trErr != nil {
			log.Error().Err(trErr).Msg("Pipeline: translation phase failed")
		}
	} else {
		skipStep(StepTranslate, "not enabled by the job config")
	}

	return runID, finalizePipelineRun(ctx, storer, notifier, jobName, start, runID, stats, auditResult,
		[]stepOutcome{
			{"targets", targetsErr}, {"news", newsErr}, {"twitter-discover", discoverErr},
			{"twitter", twitterErr}, {"frpolitics", fpErr}, {"stance-merge", mergeErr},
			{"contradictions", contraErr}, {"photos", photosErr}, {"contradiction-audit", auditErr},
			{"translate", trErr},
		})
}

// stepOutcome pairs a pipeline step with whatever it failed on (nil = fine).
type stepOutcome struct {
	step string
	err  error
}

// finalizePipelineRun classifies the run, notifies Telegram and writes the
// durable pipeline_runs summary. Shared by the normal end of a run and the
// early abort of a scoped job whose targets did not resolve, so every run —
// however it ended — leaves exactly one completed summary row.
func finalizePipelineRun(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier,
	jobName string, start time.Time, runID int64, stats *pipelineStats, auditResult *ContradictionAuditResult,
	stepErrPairs []stepOutcome) error {

	elapsed := time.Since(start)

	// Classify the run once; both the Telegram message and the pipeline_runs row
	// derive their status from these flags.
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

	// The closing writes (Telegram + the pipeline_runs summary) must land even
	// when the run was cut short by a shutdown signal — otherwise the run row
	// stays 'running' forever and trips the stuck-run watchdog on every redeploy.
	bookCtx := ctx
	if ctx.Err() != nil {
		var cancelBook context.CancelFunc
		bookCtx, cancelBook = context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancelBook()
	}

	// Notify via Telegram
	if notifier != nil {
		var msg string
		switch status {
		case "error":
			msg = fmt.Sprintf("❌ <b>Pipeline %s errors</b> (%s)\n%s",
				jobName, elapsed.Round(time.Second), strings.Join(errParts, "\n"))
		case "no_content":
			msg = fmt.Sprintf("⚠️ <b>Pipeline %s completed with no content</b> (%s)",
				jobName, elapsed.Round(time.Second))
		case "warning":
			msg = fmt.Sprintf("⚠️ <b>Pipeline %s completed with warnings</b> (%s)",
				jobName, elapsed.Round(time.Second))
		default:
			msg = fmt.Sprintf("✅ <b>Pipeline %s completed</b> (%s)",
				jobName, elapsed.Round(time.Second))
		}

		if stats != nil {
			msg += "\n" + stats.summary()
		}
		if auditResult != nil && auditResult.Total > 0 {
			msg += fmt.Sprintf("\n🔍 audit: %d checked, %d evicted (2-of-2 FP), %d kept (unstable)",
				auditResult.Total, auditResult.Deleted, auditResult.Unstable)
		}

		if err := notifier.Send(bookCtx, msg); err != nil {
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
		JobName:      jobName,
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
	if err := storer.RecordPipelineRun(bookCtx, runID, summary); err != nil {
		log.Error().Err(err).Msg("Failed to record pipeline_runs summary")
	}

	log.Info().
		Str("job", jobName).
		Dur("elapsed", elapsed).
		Int("step_errors", len(stepErrs)).
		Msg("Pipeline finished")

	return errors.Join(stepErrs...)
}

// skipStep records why a configured-away or non-applicable step did not run, so
// a job's log makes its actual coverage obvious.
func skipStep(step, reason string) {
	log.Info().Str("step", step).Str("reason", reason).Msg("Pipeline: step skipped")
}

// pipelineTargets returns the entities the fact-extraction steps (news, twitter)
// iterate over: the job's explicit targets when it has any, else the
// search_queue contents (the "main persons queue", the nightly scope).
// Every name must resolve to an existing entity — an unresolvable target is an
// error, not a silently-dropped one.
func pipelineTargets(ctx context.Context, storer *storage.PostgresStorer, cfg PipelineConfig) ([]storage.QueueEntry, error) {
	if !cfg.scoped() {
		return storer.GetSearchQueue(ctx)
	}

	entries := make([]storage.QueueEntry, 0, len(cfg.Targets))
	for _, name := range cfg.Targets {
		res := core.ResolveTargetName(ctx, storer, name)
		if res.Error != nil {
			return nil, fmt.Errorf("resolving target %q: %w", name, res.Error)
		}
		if !res.IsExactMatch {
			return nil, fmt.Errorf("target %q does not match any known entity", name)
		}
		entries = append(entries, storage.QueueEntry{
			PersonID:   *res.ExactMatch.PersonID,
			Name:       res.ExactMatch.Name,
			EntityType: res.ExactMatch.EntityType,
		})
	}
	return entries, nil
}

// runContradictionDetectionStep runs detection over the job's scope: every
// person for an unscoped job, or one pass per target for a scoped one.
func runContradictionDetectionStep(ctx context.Context, storer *storage.PostgresStorer, cfg PipelineConfig) error {
	if !cfg.scoped() {
		return executeContradictionDetection(ctx, storer, ContradictionDetectOpts{})
	}
	var errs []error
	for _, name := range cfg.Targets {
		if err := executeContradictionDetection(ctx, storer, ContradictionDetectOpts{Person: name}); err != nil {
			log.Error().Err(err).Str("person", name).Msg("Pipeline: contradiction detection failed for target")
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

// runScopedContradictionAudit audits the given targets' contradictions right
// away, bypassing the 5-day cadence gate and leaving the job_metrics stamp
// alone — that stamp paces the nightly global sweep, and an ad-hoc job must not
// consume it.
func runScopedContradictionAudit(ctx context.Context, storer *storage.PostgresStorer, targets []storage.QueueEntry) (*ContradictionAuditResult, error) {
	agg := &ContradictionAuditResult{}
	var errs []error
	for _, t := range targets {
		personID := t.PersonID
		res, err := executeContradictionAudit(ctx, storer, ContradictionAuditOpts{
			PersonID:     &personID,
			Scope:        t.Name,
			RecordMetric: false,
			Apply:        true,
		})
		if err != nil {
			log.Error().Err(err).Str("person", t.Name).Msg("Pipeline: contradiction audit failed for target")
			errs = append(errs, fmt.Errorf("%s: %w", t.Name, err))
			continue
		}
		if res != nil {
			agg.Total += res.Total
			agg.Remove += res.Remove
			agg.Deleted += res.Deleted
			agg.Unstable += res.Unstable
		}
	}
	return agg, errors.Join(errs...)
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
// The job's date window (if any) is passed straight through to the search
// provider's date filter. Returns aggregate stats and the first fatal setup
// error (per-target failures are collected, not returned).
func runNewsCollection(ctx context.Context, storer *storage.PostgresStorer, targets []storage.QueueEntry, cfg PipelineConfig) (*pipelineStats, error) {
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
		MaxParse:     cfg.maxParse(),
		Location:     cfg.location(),
		RevisitHours: 24 * 365 * 10,
		DateFrom:     cfg.DateFrom,
		DateTo:       cfg.DateTo,
		WindowDays:   cfg.WindowDays,
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

		// WindowDays is honoured by a separate entry point — CollectFacts ignores it.
		var result *news.Result
		var err error
		if newsOpts.WindowDays > 0 {
			result, err = newsProvider.CollectFactsWithDateWindows(ctx, target, newsOpts)
		} else {
			result, err = newsProvider.CollectFacts(ctx, target, newsOpts)
		}
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

// runStancesMergeStep executes the stance merge sweep step of the pipeline:
// scans each person's stances for centroid-similar candidates, LLM-vets each
// candidate cluster, applies merges, and re-detects contradictions across the
// previously-fragmented facts. Uses the CLI's default thresholds (Phase-A 0.65
// sim recall + Phase-B LLM vetting). scopeIDs restricts the sweep to those
// persons; nil sweeps every merge-candidate person.
func runStancesMergeStep(ctx context.Context, storer *storage.PostgresStorer, scopeIDs []int64) error {
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
	persons = filterPersonsByIDs(persons, scopeIDs)

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

// filterPersonsByIDs narrows a person list to the given ids. A nil/empty id list
// means "no filter" (the nightly global scope).
func filterPersonsByIDs(persons []storage.PersonInfo, ids []int64) []storage.PersonInfo {
	if len(ids) == 0 {
		return persons
	}
	keep := make(map[int64]bool, len(ids))
	for _, id := range ids {
		keep[id] = true
	}
	out := make([]storage.PersonInfo, 0, len(ids))
	for _, p := range persons {
		if keep[p.ID] {
			out = append(out, p)
		}
	}
	return out
}

// runTwitterCollection fetches tweets for active Twitter handles whose person is
// in targetIDs and extracts facts, restricted to the job's date window when it
// has one. Gracefully skips if no handles exist or if the Twitter API key is not
// configured.
func runTwitterCollection(ctx context.Context, storer *storage.PostgresStorer, targetIDs map[int64]bool, cfg PipelineConfig) error {
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
		MaxTweets:  50,
		BatchSize:  20,
		DateFrom:   cfg.DateFrom,
		DateTo:     cfg.DateTo,
		WindowDays: cfg.WindowDays,
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
		// WindowDays is honoured by a separate entry point — CollectFacts ignores it.
		var result *twitter.Result
		var err error
		if opts.WindowDays > 0 {
			result, err = provider.CollectFactsWithDateWindows(ctx, handle, opts)
		} else {
			result, err = provider.CollectFacts(ctx, handle, opts)
		}
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

// runPhotosBackfill searches and uploads profile photos for persons/parties that
// don't have one yet — restricted to targetIDs when the job is scoped. Gracefully
// no-ops if image search or R2 is not configured (a degraded but non-fatal state).
func runPhotosBackfill(ctx context.Context, storer *storage.PostgresStorer, targetIDs map[int64]bool, scoped bool) error {
	persons, err := storer.GetPersonsWithoutImage(ctx)
	if err != nil {
		return fmt.Errorf("failed to get persons without image: %w", err)
	}
	if scoped {
		kept := make([]storage.PersonSummary, 0, len(targetIDs))
		for _, p := range persons {
			if targetIDs[p.ID] {
				kept = append(kept, p)
			}
		}
		persons = kept
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
