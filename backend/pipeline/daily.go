package pipeline

import (
	"context"
	"digital-mirror/backend/metrics"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"digital-mirror/backend/alerting"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/translate"

	"github.com/rs/zerolog/log"
)

// Nightly budgets. Each one bounds a phase so a run finishes in one night;
// the pending work carries over to the next run (every phase derives its
// pending set from the database, so nothing is lost).
const (
	// pairRejudgeNightlyBudget caps how many stale negative pair verdicts are
	// re-opened per run after a rulebook change (~4.5 h of GPU at the
	// measured pace).
	pairRejudgeNightlyBudget = 4500
	// translateNightlyLimit caps how many translations the final phase fills
	// per run, so a large first-time backlog drains over several runs.
	translateNightlyLimit = 4500
	// auditCadence is how often the contradiction auditor re-judges stored
	// contradictions.
	auditCadence = 5 * 24 * time.Hour
	// No phase starts after runDeadlineHour in runTimezone, and the backfill
	// (the last phase) stops starting work then: the cron starts at 01:00 UTC
	// and the user lets the run use the GPU through the day, but it must end
	// before the next cron — the host skips a scheduled run while the previous
	// one is still active.
	runDeadlineHour = 23
	runTimezone     = "Europe/Paris"
)

// RunOptions tunes one daily run.
type RunOptions struct {
	// Smoke bounds every phase to a handful of items (one target, one item
	// per plugin, one person per sweep, no audit, no backfill) and records no
	// pipeline_runs row, so a deploy can be exercised end to end in minutes
	// without touching the run history the watchdog reads.
	Smoke bool
}

// RunDaily executes the daily pipeline once and returns an aggregated error
// if any step failed. Designed to be invoked by `dm pipeline daily`, which
// the host cron triggers.
//
// Phases in order:
//  1. ingest                    — every plugin (queue-scoped targets)
//  2. stance fan-out            — unrouted (person, fact) pairs into that person's stances
//  3. stance merge sweep
//  4. stale-verdict eviction + plugin pair production + contradiction detection
//  5. duplicate repoint + contradiction regroup
//  6. contradiction audit (5-day cadence)
//  7. translation phase
//  8. backfill                  — every plugin, deadline-bound
//
// Once a phase reports the LLM unavailable (llm.ErrUnavailable) no further
// LLM phase starts: every item would fail the same way, and walking them
// only burns scrape credits and hours. The phases that need no LLM still run,
// and the run is recorded as failed ("LLM unavailable: stopped after phase
// X"). Every phase derives its pending work from the database, so the next
// run resumes where this one stopped.
func RunDaily(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier, opts RunOptions) error {
	start := time.Now()
	log.Info().Bool("smoke", opts.Smoke).Msg("Daily pipeline started")
	d := deps.New(storer)
	b := budgets(opts)

	// Record the run's existence BEFORE doing anything, so a killed or hung run
	// leaves a 'running' row instead of vanishing (mirror-bp0). Best-effort:
	// runID 0 makes RecordPipelineRun fall back to a full INSERT at the end.
	var runID int64
	if !opts.Smoke {
		var err error
		if runID, err = storer.StartPipelineRun(ctx, start); err != nil {
			log.Error().Err(err).Msg("Daily pipeline: failed to record run start; summary will be inserted at end")
		}
	}

	// Probe the primary LLM endpoint once. If the home box is unreachable, arm
	// the circuit breaker now so EVERY call this run goes straight to the
	// fallback instead of burning the full retry backoff per call — an outage
	// used to turn 3-hour runs into 10-hour ones (mirror-s8t).
	if !llm.ProbePrimary(ctx) {
		log.Warn().Msg("Daily pipeline: primary LLM endpoint unreachable — routing the entire run to the fallback")
	}

	stepErrs := map[string]error{}
	var gate runGate
	fail := func(step string, err error, msg string) {
		if err == nil {
			return
		}
		gate.observe(step, err)
		stepErrs[step] = err
		log.Error().Err(err).Msg("Daily pipeline: " + msg)
	}
	deadline, dErr := runDeadline(start)
	if dErr != nil {
		// Without a deadline the backfill would run unbounded into the next cron.
		fail("deadline", dErr, "run deadline unknown; backfill skipped")
		b.backfill = false
	}
	gate.deadline = deadline

	// The judgement counters this run produces are flushed as it goes, not
	// only at the end: the cron process is not scraped, and a run that dies
	// mid-night (an outage) would otherwise take the night's counts with it.
	metrics.StartDecisionFlusher(ctx, storer, time.Minute)

	// Targets for the queue-scoped plugins: the search_queue contents (the
	// "main persons queue"). Plugins with their own scope ignore them.
	targets, err := QueueTargets(ctx, storer)
	fail("targets", err, "failed to resolve target persons")
	if b.maxTargets > 0 && len(targets) > b.maxTargets {
		targets = targets[:b.maxTargets]
	}
	log.Info().Int("targets", len(targets)).Msg("Daily pipeline: target persons resolved")

	// Phase 1: ingest. Every plugin collects its new facts; stance routing and
	// the analyze judge run inline on insert.
	ingest, ingestErrs, err := Ingest(ctx, d, targets, plugins.IngestOptions{MaxItems: b.maxItems}, nil)
	fail("ingest", err, "ingest phase failed")
	for _, pErr := range ingestErrs {
		gate.observe("ingest", pErr)
	}
	mergeStepErrs(stepErrs, ingestErrs)
	stats := ingest.Total

	stanceMgr, err := d.StanceManager()
	if err != nil {
		// Every remaining phase needs the stance manager; record the failure
		// once and skip them rather than fail each one with the same cause.
		fail("stance-manager", err, "stance manager unavailable; skipping the stance and detection phases")
	} else {
		rebundle, rbErr := personRebundler(d)
		fail("bundler", rbErr, "contradiction bundler unavailable")

		// Phase 2: stance fan-out (mirror-py3). Routes every (person, fact)
		// mention pair missing from that person's stances into their own
		// stance system (budgeted per night, derived pending set,
		// self-terminating). Runs before the merge sweep
		// so freshly created deputy stances get merged the same night, and
		// before detection so stance-recorded contradictions land first.
		if rebundle != nil && gate.open("stance-fanout") {
			fail("stance-fanout", core.RunStanceFanout(ctx, storer, stanceMgr, rebundle, b.fanout), "stance fan-out failed")
		}

		// Phase 3: stance merge sweep. Looks for stances that should be merged
		// per LLM-vetted centroid clustering, applies the merges, and re-detects
		// contradictions across previously-separated facts. Catches the
		// cross-stance contradictions our per-fact assignTopic missed by
		// mis-grouping.
		if gate.open("stance-merge") {
			mergeOpts := core.DefaultStanceMergeOptions
			mergeOpts.MaxPersons = b.maxPersons
			_, err = core.RunStanceMergeSweep(ctx, storer, stanceMgr, mergeOpts)
			fail("stance-merge", err, "stance merge sweep failed")
		}
	}

	// Phase 4: contradiction detection over unchecked pairs across all
	// persons, with the inline verification pass gating each YES before it is
	// recorded. Re-judge of stale negatives: a NO is only final under the
	// rulebook that produced it. Every negative verdict carries the judge
	// rulebook hash; when the prompts change, the oldest stale negatives are
	// re-opened each night (pairRejudgeNightlyBudget) right before detection,
	// so the whole backlog of NOs is re-judged within days — the counterpart
	// of the auditor's last_audit_hash for YESes. Self-terminating: with
	// nothing stale, silent. Skipped with the detection it feeds when the LLM
	// is unavailable: re-opened pairs would pile onto the next night's budget.
	detect := gate.open("contradictions")
	if detect && b.rejudge > 0 {
		judgeHash := contradiction.JudgeRulebookHash()
		if deleted, remaining, evErr := storer.EvictStaleJudgedPairs(ctx, judgeHash, b.rejudge); evErr != nil {
			log.Error().Err(evErr).Msg("Daily pipeline: stale pair-verdict eviction failed")
		} else if deleted > 0 || remaining > 0 {
			log.Info().Int("reopened", deleted).Int("stale_remaining", remaining).Str("judge_hash", judgeHash).
				Msg("Daily pipeline: re-opened stale negative pair verdicts for re-judging under the current rulebook")
		}
	}

	// Plugins queue the structural pairs the detector cannot reach by
	// embedding distance. After the eviction so a re-opened pair is queued
	// tonight rather than tomorrow.
	queued, pairErrs := ProducePairs(ctx, d)
	mergeStepErrs(stepErrs, pairErrs)
	if total, cErr := storer.CountQueuedPairs(ctx); cErr == nil && (queued > 0 || total > 0) {
		log.Info().Int("enqueued", queued).Int("queue_size", total).Msg("Daily pipeline: fact pairs queued for detection")
	}

	if detect {
		_, err = contradiction.RunDetection(ctx, storer, contradiction.DetectOptions{MaxPersons: b.maxPersons})
		fail("contradictions", err, "contradiction detection failed")
	}

	// Phase 5a: repoint contradictions off any facts the contradiction-pass
	// dedup just marked as duplicates, so the redundant rows stop inflating
	// scores. Best-effort — failures are logged, never aborting the pipeline.
	if processed, repointed, dropped, rErr := storer.RepointDuplicates(ctx, true); rErr != nil {
		log.Error().Err(rErr).Msg("Daily pipeline: repoint-duplicates failed")
	} else if processed > 0 {
		log.Info().Int("duplicates", processed).Int("repointed", repointed).Int("dropped", dropped).
			Msg("Daily pipeline: repointed contradictions off duplicate facts")
	}

	// Phase 5b: regroup contradictions into N-ary groups (one base fact + N
	// contradicting facts) and (re)generate each group's merged summary. Runs
	// after detection + duplicate-repointing so it sees the final pairwise
	// set. Best-effort — failures are logged, never aborting the pipeline,
	// except an unavailable LLM, which stops the run's LLM phases.
	if gate.open("regroup") {
		if regroupErr := contradiction.RunRegroupStep(ctx, storer, b.maxPersons); regroupErr != nil {
			gate.observe("regroup", regroupErr)
			log.Error().Err(regroupErr).Msg("Daily pipeline: contradiction regroup failed")
		}
	}

	// Phase 6: contradiction auditor (auditCadence). Re-judges previously
	// recorded contradictions and AUTO-EVICTS confirmed false positives
	// (Apply:true). A row is deleted only when THREE signals agree: the audit
	// verdict is remove, a confirming second audit pass also says remove, and
	// the verifier (refute pass — a different, reversal-aware judge) also
	// drops it (cross-judge veto; see applyAuditRemoval). Any disagreement or
	// error keeps the row and counts it as unstable. Verdicts are also
	// exposed via the `contradiction_audits_total` Prometheus counter.
	var auditResult *contradiction.AuditRunResult
	if b.audit && gate.open("contradiction-audit") {
		auditResult, err = runAuditIfDue(ctx, storer, auditCadence)
		fail("contradiction-audit", err, "contradiction audit failed")
	}

	// Phase 7: translation — decoupled from generation. Fills missing or
	// stale translations for facts, stances and contradictions into the
	// generic translations table for every target language.
	if gate.open("translate") {
		fail("translate", runTranslate(ctx, storer, b.translate), "translation phase failed")
	}

	// Phase 8 (last, deadline-bound): plugin backfills; only those that need
	// no LLM once it is unavailable.
	if b.backfill {
		var only []string
		if !gate.open("backfill") {
			only = llmFreeBackfills
		}
		bfErrs := Backfill(ctx, d, deadline, only)
		for _, pErr := range bfErrs {
			gate.observe("backfill", pErr)
		}
		mergeStepErrs(stepErrs, bfErrs)
	}
	if gErr := gate.err(); gErr != nil {
		stepErrs["llm"] = gErr
		log.Error().Err(gErr).Msg("Daily pipeline: LLM unavailable; the next run resumes the skipped phases")
	}

	elapsed := time.Since(start)

	// Classify the run once; both the Telegram message and the pipeline_runs
	// row derive their status from these flags.
	stepNames := make([]string, 0, len(stepErrs))
	for name := range stepErrs {
		stepNames = append(stepNames, name)
	}
	sort.Strings(stepNames)
	var errs []error
	var errParts []string
	for _, name := range stepNames {
		errs = append(errs, stepErrs[name])
		errParts = append(errParts, name+": "+stepErrs[name].Error())
	}
	status := "success"
	switch {
	case len(errs) > 0:
		status = "error"
	case stats.Targets > 0 && stats.Facts == 0:
		status = "no_content"
	case stats.Errors > 0 || len(stats.Details) > 0:
		status = "warning"
	}

	if notifier != nil {
		if err := notifier.Send(ctx, notification(status, opts.Smoke, elapsed, errParts, stats, auditResult)); err != nil {
			log.Error().Err(err).Msg("Failed to send pipeline Telegram notification")
		}
	}

	// Persist a durable per-run summary for Grafana (Postgres datasource). The
	// cron process is ephemeral so Prometheus cannot scrape it; this row is
	// the source of truth for pipeline-run history. Best-effort — a write
	// failure is logged but never fails the run.
	if !opts.Smoke {
		summary := storage.PipelineRunSummary{
			StartedAt:    start,
			FinishedAt:   time.Now(),
			Status:       status,
			StepErrors:   len(errs),
			ErrorSummary: strings.Join(errParts, "\n"),
			// The row's columns keep their historical meaning for the Grafana
			// dashboards: the queue size, the news plugin's parsed articles and
			// its scrape errors, not the sums over every plugin.
			Targets:      len(targets),
			NewsArticles: ingest.ByPlugin["news"].Items,
			ScrapeErrors: ingest.ByPlugin["news"].Errors,
		}
		if auditResult != nil {
			summary.AuditChecked = auditResult.Total
			summary.AuditFlagged = auditResult.Remove
		}
		if err := storer.RecordPipelineRun(ctx, runID, summary); err != nil {
			log.Error().Err(err).Msg("Failed to record pipeline_runs summary")
		}
	}

	log.Info().Dur("elapsed", elapsed).Int("step_errors", len(errs)).Str("status", status).Msg("Daily pipeline finished")
	return errors.Join(errs...)
}

// runBudgets are the per-phase bounds of one run.
type runBudgets struct {
	maxTargets int  // 0 = the whole queue
	maxItems   int  // per-target item cap for plugins; 0 = plugin default
	fanout     int  // (person, fact) stance routings
	maxPersons int  // persons per sweep (merge, detection, regroup); 0 = all
	rejudge    int  // stale negative verdicts re-opened; 0 = skip
	audit      bool // run the auditor when due
	translate  int  // translations filled
	backfill   bool // run the deadline-bound backfill phase
}

func budgets(opts RunOptions) runBudgets {
	if opts.Smoke {
		return runBudgets{maxTargets: 1, maxItems: 1, fanout: 3, maxPersons: 1, translate: 5}
	}
	return runBudgets{
		fanout:    core.StanceFanoutNightlyBudget,
		rejudge:   pairRejudgeNightlyBudget,
		audit:     true,
		translate: translateNightlyLimit,
		backfill:  true,
	}
}

// QueueTargets converts the search_queue contents into plugin targets.
func QueueTargets(ctx context.Context, storer *storage.PostgresStorer) ([]plugins.Target, error) {
	queue, err := storer.GetSearchQueue(ctx)
	if err != nil {
		return nil, err
	}
	targets := make([]plugins.Target, 0, len(queue))
	for _, e := range queue {
		targets = append(targets, plugins.Target{PersonID: e.PersonID, Name: e.Name, EntityType: e.EntityType})
	}
	return targets, nil
}

// personRebundler builds the per-person contradiction rebundling callback the
// stance sweeps take after they change a person's stances.
func personRebundler(d *deps.Deps) (core.PersonRebundler, error) {
	embedder, err := d.Embedder()
	if err != nil {
		return nil, fmt.Errorf("embedder: %w", err)
	}
	bundler, err := contradiction.NewBundlerFromEnv(d.Storer, embedder)
	if err != nil {
		return nil, fmt.Errorf("bundler: %w", err)
	}
	storer := d.Storer
	return func(ctx context.Context, p core.PersonRecord) (fast, llm, failed int64) {
		return contradiction.RebundlePerson(ctx, storer, bundler, storage.PersonInfo{ID: p.ID, Name: p.Name, EntityType: p.EntityType})
	}, nil
}

// runAuditIfDue runs the contradiction auditor at most once per cadence. The
// cadence is enforced via the job_metrics timestamp for
// job_type="contradiction-audit": last-run is read at the start, and an audit
// that judged its rows (RecordMetric) writes a new row at the end; a failed
// audit writes none, so the next run tries again. Returns nil when skipped.
func runAuditIfDue(ctx context.Context, storer *storage.PostgresStorer, cadence time.Duration) (*contradiction.AuditRunResult, error) {
	last, err := storer.GetLatestJobRunTime(ctx, "contradiction-audit")
	if err != nil {
		// Reading the timestamp failed: log and skip (don't keep retrying every
		// daily tick; the next successful read will get the audit going again).
		log.Error().Err(err).Msg("Contradiction audit: failed to read last-run timestamp, skipping this cycle")
		return nil, nil
	}
	if last != nil && time.Since(*last) < cadence {
		log.Info().Time("last_run", *last).Dur("cadence", cadence).Dur("age", time.Since(*last)).
			Msg("Contradiction audit: not due yet, skipping")
		return nil, nil
	}
	log.Info().Dur("cadence", cadence).Msg("Contradiction audit: due, starting pass")
	return contradiction.RunAudit(ctx, storer, contradiction.AuditOptions{
		Scope:        "all persons (periodic)",
		RecordMetric: true,
		// Auto-evict: a verdict=remove confirmed by the independent passes is
		// deleted via DismissContradictionAsFalsePositive (negative pair check
		// + score recompute), so false positives never accumulate between runs.
		Apply: true,
	})
}

// runTranslate is the final translation phase; limit caps the fill so a large
// first-time backlog drains over several runs.
func runTranslate(ctx context.Context, storer *storage.PostgresStorer, limit int) error {
	tr, err := translate.NewOpenAITranslatorFromEnv()
	if err != nil {
		return fmt.Errorf("translator unavailable: %w", err)
	}
	_, err = translate.Fill(ctx, storer, tr, translate.FillOptions{Limit: limit})
	return err
}

// runGate decides whether a phase may start: not once a phase reported the
// LLM unavailable (llm.ErrUnavailable), and not after the run deadline.
type runGate struct {
	stoppedAfter string    // the phase that hit the outage; "" while the LLM is up
	deadline     time.Time // zero: no deadline
}

func (g *runGate) observe(phase string, err error) {
	if g.stoppedAfter == "" && errors.Is(err, llm.ErrUnavailable) {
		g.stoppedAfter = phase
	}
}

// open reports whether the phase may start; it logs the skip when not.
func (g *runGate) open(phase string) bool {
	if g.stoppedAfter != "" {
		log.Warn().Str("phase", phase).Str("stopped_after", g.stoppedAfter).
			Msg("Daily pipeline: LLM unavailable; phase not started")
		return false
	}
	if !g.deadline.IsZero() && time.Now().After(g.deadline) {
		log.Warn().Str("phase", phase).Time("deadline", g.deadline).
			Msg("Daily pipeline: run deadline reached; phase not started")
		return false
	}
	return true
}

// err is the run-level error of a run the gate stopped, nil otherwise.
func (g *runGate) err() error {
	if g.stoppedAfter == "" {
		return nil
	}
	return fmt.Errorf("%w: stopped after phase %s", llm.ErrUnavailable, g.stoppedAfter)
}

func mergeStepErrs(dst map[string]error, src StepErrors) {
	for name, err := range src {
		if prev, ok := dst[name]; ok {
			dst[name] = errors.Join(prev, err)
			continue
		}
		dst[name] = err
	}
}

// runDeadline is the first runDeadlineHour in runTimezone after start.
func runDeadline(start time.Time) (time.Time, error) {
	loc, err := time.LoadLocation(runTimezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("loading %s timezone: %w", runTimezone, err)
	}
	s := start.In(loc)
	d := time.Date(s.Year(), s.Month(), s.Day(), runDeadlineHour, 0, 0, 0, loc)
	if !d.After(s) {
		d = d.AddDate(0, 0, 1)
	}
	return d, nil
}

func notification(status string, smoke bool, elapsed time.Duration, errParts []string, s plugins.IngestStats, audit *contradiction.AuditRunResult) string {
	prefix := ""
	if smoke {
		prefix = "[smoke] "
	}
	var msg string
	switch status {
	case "error":
		msg = fmt.Sprintf("❌ <b>%sDaily pipeline errors</b> (%s)\n%s", prefix, elapsed.Round(time.Second), strings.Join(errParts, "\n"))
	case "no_content":
		msg = fmt.Sprintf("⚠️ <b>%sDaily pipeline completed with no content</b> (%s)", prefix, elapsed.Round(time.Second))
	case "warning":
		msg = fmt.Sprintf("⚠️ <b>%sDaily pipeline completed with warnings</b> (%s)", prefix, elapsed.Round(time.Second))
	default:
		msg = fmt.Sprintf("✅ <b>%sDaily pipeline completed</b> (%s)", prefix, elapsed.Round(time.Second))
	}
	msg += fmt.Sprintf("\ntargets=%d items=%d facts=%d item_errors=%d", s.Targets, s.Items, s.Facts, s.Errors)
	if len(s.Details) > 0 {
		msg += fmt.Sprintf("\n⚠️ Collection errors (%d):", len(s.Details))
		for _, e := range s.Details {
			msg += "\n  • " + e
		}
	}
	if audit != nil && audit.Total > 0 {
		msg += fmt.Sprintf("\n🔍 audit: %d checked, %d evicted (2-of-2 FP), %d kept (unstable)", audit.Total, audit.Deleted, audit.Unstable)
	}
	return msg
}
