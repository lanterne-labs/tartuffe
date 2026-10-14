package pipeline

import (
	"context"
	"digital-mirror/backend/metrics"
	"errors"
	"fmt"
	"html"
	"sort"
	"strings"
	"sync"
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
	// stanceRefreshNightlyBudget caps how many stances the refresh phase
	// brings up to date per run (one LLM call each). The stances touched by
	// the ingest come first; an older backlog drains over several runs.
	stanceRefreshNightlyBudget = 2000
	// auditCadence is how often the contradiction auditor re-judges stored
	// contradictions.
	auditCadence = 5 * 24 * time.Hour
	// The run's work context is cancelled at runDeadlineHour in runTimezone:
	// every phase and every plugin stops where it is, and the next run resumes
	// from the pending sets in the database. The cron starts at 01:00 UTC and
	// the user lets the run use the GPU through the day, but it must end
	// before the next cron — the host skips a scheduled run while the previous
	// one is still active.
	runDeadlineHour = 23
	// ingestBudget bounds the ingest phase from the run start, so detection,
	// the audit and the translations get the rest of the day. A vote law
	// cost ~65 min of GPU when every ballot had the full stance routing, and
	// an ingest with no bound of its own ran 28 h (run 111).
	ingestBudget = 12 * time.Hour
	// mergeBudget bounds the stance merge sweep the same way: it visits every
	// person and took most of the afternoon of run 112, leaving detection,
	// the audit and the history no time. Swept persons are memoized, so a cut
	// sweep resumes with the next person on the next run.
	mergeBudget = 2 * time.Hour
	// stanceRefreshBudget bounds the stance refresh phase in time, on top of
	// stanceRefreshNightlyBudget. A refresh is one LLM call of about 1.5 s on
	// the local GPU (a vote law of ~577 ballots at 4 to 7 calls each took ~65
	// min), so 2000 stances take about 50 min. With ingest (12 h) and merge
	// (2 h), at least 5 h of the 20 h run stay for detection, the audit and
	// the translations, also on a slower endpoint.
	stanceRefreshBudget = 1 * time.Hour
	runTimezone         = "Europe/Paris"
)

// RunOptions tunes one daily run.
type RunOptions struct {
	// Smoke bounds every phase to a handful of items (one target, one item
	// per plugin, one person per sweep, no audit, no backfill) and records no
	// pipeline_runs row, so a deploy can be exercised end to end in minutes
	// without touching the run history the watchdog reads.
	Smoke bool
}

// creditAlert returns the llm.OnCreditError hook of a run: the first credit
// error of each provider sends one message with send. The later ones of the
// same provider send nothing, so a run with many failed calls does not flood
// the channel.
func creditAlert(send func(context.Context, string) error) func(provider, errMsg string) {
	var mu sync.Mutex
	sent := map[string]bool{}
	return func(provider, errMsg string) {
		mu.Lock()
		first := !sent[provider]
		sent[provider] = true
		mu.Unlock()
		if !first {
			return
		}
		if len(errMsg) > 300 {
			errMsg = errMsg[:300] + "..."
		}
		// Telegram parses the message as HTML: the error text is escaped.
		msg := fmt.Sprintf("<b>LLM credit error</b>\nProvider: %s\nError: %s", html.EscapeString(provider), html.EscapeString(errMsg))
		if err := send(context.Background(), msg); err != nil {
			log.Error().Err(err).Str("provider", provider).Msg("Failed to send the LLM credit alert")
		}
	}
}

// RunDaily executes the daily pipeline once and returns an aggregated error
// if any step failed. Designed to be invoked by `dm pipeline daily`, which
// the host cron triggers.
//
// Phases in order:
//  1. ingest                    — every plugin (queue-scoped targets)
//  2. stance refresh            — the stances with pending facts
//  3. stance merge sweep
//  4. stale-verdict eviction + plugin pair production + contradiction detection
//  5. duplicate repoint + contradiction regroup
//  6. contradiction audit (5-day cadence)
//  7. translation phase
//  8. history                   — date-range plugins, one month further back
//  9. backfill                  — every plugin, deadline-bound
//
// When a phase reports the LLM unavailable (llm.ErrUnavailable) the run waits
// for the endpoint that failed (llm.WaitForEndpoint) until the run deadline,
// at most maxLLMWait over the whole run, then runs the phase again: every
// phase derives its pending work from the database, so the retry resumes
// where the outage stopped it (ingest and backfill skip the plugins and
// targets already done; budgets carry what was spent). A run that waited and
// recovered is not a failed run; its outages and time waited are noted in the
// notification and the pipeline_runs row. If the LLM is not back before the
// deadline or the end of the wait budget, no further LLM phase (re)starts and
// the run is recorded as failed ("LLM unavailable: stopped after phase X");
// the next run resumes. A phase that keeps failing while the LLM answers is
// given up after maxQuickRefails retries, as a step error, and the run goes
// on. A smoke run does not wait.
func RunDaily(ctx context.Context, storer *storage.PostgresStorer, notifier *alerting.TelegramNotifier, opts RunOptions) error {
	start := time.Now()
	log.Info().Bool("smoke", opts.Smoke).Msg("Daily pipeline started")
	// The tokens and the cost per task of the Claude endpoints of the run.
	defer llm.LogUsage()
	// A credit error of an LLM provider (or the spend cap of the Claude API)
	// sends 1 Telegram message per provider, as the web service does.
	if notifier != nil {
		llm.OnCreditError = creditAlert(notifier.Send)
		defer func() { llm.OnCreditError = nil }()
	}
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
	if !opts.Smoke {
		gate.waitLLM = llm.WaitForEndpoint
	}
	var cutSteps []string // phases stopped by their deadline: not errors, resumed next run
	// fail records a step's error; the parts of it that are the step's
	// context expiring (ctx: the run's work context, or the ingest phase's)
	// are a deadline cut, a note, and the rest a step error.
	fail := func(ctx context.Context, step string, err error, msg string) {
		cut, err := splitCut(ctx, err)
		if cut {
			cutSteps = append(cutSteps, step)
			log.Warn().Str("step", step).Msg("Daily pipeline: stopped at its deadline; the next run resumes it")
		}
		if err == nil {
			return
		}
		log.Error().Err(err).Msg("Daily pipeline: " + msg)
		if prev, ok := stepErrs[step]; ok {
			err = errors.Join(prev, err)
		}
		stepErrs[step] = err
	}
	mergeErrs := func(ctx context.Context, src StepErrors) {
		for name, err := range src {
			fail(ctx, name, err, "step failed")
		}
	}
	deadline, dErr := runDeadline(start)
	if dErr != nil {
		// Without a deadline the backfill would run unbounded into the next cron.
		fail(ctx, "deadline", dErr, "run deadline unknown; history and backfill skipped")
		b.history = false
		b.backfill = false
	}
	gate.deadline = deadline

	// work is the context of every phase: cancelled at the run deadline, so
	// a phase or plugin still running then stops where it is. ctx stays live
	// for the bookkeeping after the phases (notification, pipeline_runs row).
	work := ctx
	if !deadline.IsZero() {
		var cancel context.CancelFunc
		work, cancel = context.WithDeadline(ctx, deadline)
		defer cancel()
	}

	// The judgement counters this run produces are flushed as it goes, not
	// only at the end: the cron process is not scraped, and a run that dies
	// mid-night (an outage) would otherwise take the night's counts with it.
	metrics.StartDecisionFlusher(ctx, storer, time.Minute)

	// Targets for the queue-scoped plugins: the search_queue contents (the
	// "main persons queue"). Plugins with their own scope ignore them.
	targets, err := QueueTargets(work, storer)
	fail(work, "targets", err, "failed to resolve target persons")
	if b.maxTargets > 0 && len(targets) > b.maxTargets {
		targets = targets[:b.maxTargets]
	}
	log.Info().Int("targets", len(targets)).Msg("Daily pipeline: target persons resolved")

	// Phase 1: ingest. Every plugin collects its new facts; stance routing
	// runs on insert. The analyze judge and the refresh of the label and
	// summary run inline, except for a plugin that places its facts with
	// ApplyStanceDecision (frpolitics votes): their stances are refreshed in
	// phase 2.
	// A plugin given up (errRefails) is abandoned and the phase goes on with
	// the next plugins. An outage error is already in ingest.Errs, under its
	// plugin.
	ingestCtx, cancelIngest := context.WithTimeout(work, ingestBudget)
	defer cancelIngest()
	var ingest IngestRun
	for {
		ingestErr := gate.run(ingestCtx, "ingest", func() error {
			return ingest.Run(ingestCtx, d, targets, plugins.IngestOptions{MaxItems: b.maxItems}, nil)
		})
		if errors.Is(ingestErr, errRefails) && ingest.Abandon() {
			continue
		}
		if !errors.Is(ingestErr, llm.ErrUnavailable) {
			fail(ingestCtx, "ingest", ingestErr, "ingest phase failed")
		}
		break
	}
	mergeErrs(ingestCtx, ingest.Errs)
	stats := ingest.Result.Total

	stanceMgr, err := d.StanceManager()
	if err != nil {
		// Every remaining phase needs the stance manager; record the failure
		// once and skip them rather than fail each one with the same cause.
		fail(work, "stance-manager", err, "stance manager unavailable; skipping the stance and detection phases")
	} else {
		// Phase 2: stance refresh. It brings the label and summary of each
		// stance with pending facts up to date once per run, before the merge
		// sweep reads them. Bounded by count and by time.
		refreshCtx, cancelRefresh := context.WithTimeout(work, stanceRefreshBudget)
		defer cancelRefresh()
		fail(refreshCtx, "stance-refresh", gate.run(refreshCtx, "stance-refresh", budgeted(b.refresh, func(left int) (int, error) {
			return core.RefreshPendingStances(refreshCtx, storer, stanceMgr, left)
		})), "stance refresh failed")

		// Phase 3: stance merge sweep. Looks for stances that should be merged
		// per LLM-vetted centroid clustering, applies the merges, and re-detects
		// contradictions across previously-separated facts. Catches the
		// cross-stance contradictions our per-fact assignTopic missed by
		// mis-grouping.
		mergeOpts := core.DefaultStanceMergeOptions
		mergeOpts.MaxPersons = b.maxPersons
		mergeCtx, cancelMerge := context.WithTimeout(work, mergeBudget)
		defer cancelMerge()
		fail(mergeCtx, "stance-merge", gate.run(mergeCtx, "stance-merge", func() error {
			_, err := core.RunStanceMergeSweep(mergeCtx, storer, stanceMgr, mergeOpts)
			return err
		}), "stance merge sweep failed")
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
	// Not repeated when detection is retried after an outage: the re-opened
	// pairs are still pending, and the budget is per run.
	detect := gate.open("contradictions")
	if detect && b.rejudge > 0 {
		judgeHash := contradiction.JudgeRulebookHash()
		if deleted, remaining, evErr := storer.EvictStaleJudgedPairs(work, judgeHash, b.rejudge); evErr != nil {
			log.Error().Err(evErr).Msg("Daily pipeline: stale pair-verdict eviction failed")
		} else if deleted > 0 || remaining > 0 {
			log.Info().Int("reopened", deleted).Int("stale_remaining", remaining).Str("judge_hash", judgeHash).
				Msg("Daily pipeline: re-opened stale negative pair verdicts for re-judging under the current rulebook")
		}
	}

	// Plugins queue the structural pairs the detector cannot reach by
	// embedding distance. After the eviction so a re-opened pair is queued
	// tonight rather than tomorrow.
	queued, pairErrs := ProducePairs(work, d)
	mergeErrs(work, pairErrs)
	if total, cErr := storer.CountQueuedPairs(work); cErr == nil && (queued > 0 || total > 0) {
		log.Info().Int("enqueued", queued).Int("queue_size", total).Msg("Daily pipeline: fact pairs queued for detection")
	}

	if detect {
		fail(work, "contradictions", gate.retry(work, "contradictions", func() error {
			_, err := contradiction.RunDetection(work, storer, contradiction.DetectOptions{MaxPersons: b.maxPersons})
			return err
		}), "contradiction detection failed")
	}

	// Phase 5a: repoint contradictions off any facts the contradiction-pass
	// dedup just marked as duplicates, so the redundant rows stop inflating
	// scores. Best-effort — failures are logged, never aborting the pipeline.
	if processed, repointed, dropped, rErr := storer.RepointDuplicates(work, true); rErr != nil {
		log.Error().Err(rErr).Msg("Daily pipeline: repoint-duplicates failed")
	} else if processed > 0 {
		log.Info().Int("duplicates", processed).Int("repointed", repointed).Int("dropped", dropped).
			Msg("Daily pipeline: repointed contradictions off duplicate facts")
	}

	// Phase 5b: regroup contradictions into N-ary groups (one base fact + N
	// contradicting facts) and (re)generate each group's merged summary. Runs
	// after detection + duplicate-repointing so it sees the final pairwise
	// set. Best-effort — failures are logged, never aborting the pipeline;
	// an unavailable LLM is waited for like in every LLM phase.
	if regroupErr := gate.run(work, "regroup", func() error {
		return contradiction.RunRegroupStep(work, storer, b.maxPersons)
	}); regroupErr != nil {
		cut, rest := splitCut(work, regroupErr)
		if cut {
			cutSteps = append(cutSteps, "regroup")
		}
		if rest != nil {
			log.Error().Err(rest).Msg("Daily pipeline: contradiction regroup failed")
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
	if b.audit {
		fail(work, "contradiction-audit", gate.run(work, "contradiction-audit", func() error {
			res, err := runAuditIfDue(work, storer, auditCadence)
			auditResult = mergeAuditTries(auditResult, res)
			return err
		}), "contradiction audit failed")
	}

	// Phase 7: translation — decoupled from generation. Fills missing or
	// stale translations for facts, stances and contradictions into the
	// generic translations table for every target language.
	fail(work, "translate", gate.run(work, "translate", budgeted(b.translate, func(left int) (int, error) {
		return runTranslate(work, storer, left)
	})), "translation phase failed")

	// Phase 8: history. Every plugin that reads its source by date range goes
	// one month further back for the queue targets (see runHistory). Before
	// the backfill, which takes whatever time is left.
	if b.history {
		mergeErrs(work, runHistory(work, d, storer, targets, start, func(phase string, fn func() error) error {
			return gate.run(work, phase, fn)
		}))
	}

	// Phase 9 (last, deadline-bound): plugin backfills. An outage the run did
	// not recover from is already in backfill.Errs, under its plugin.
	if b.backfill {
		var backfill BackfillRun
		for {
			bfErr := gate.run(work, "backfill", func() error { return backfill.Run(work, d, deadline) })
			if !errors.Is(bfErr, errRefails) || !backfill.Abandon() {
				break
			}
		}
		mergeErrs(work, backfill.Errs)
	}
	if gErr := gate.err(); gErr != nil {
		stepErrs["llm"] = gErr
		log.Error().Err(gErr).Msg("Daily pipeline: LLM unavailable; the next run resumes the skipped phases")
	}
	llmNote := gate.note()
	var notes []string
	if llmNote != "" {
		notes = append(notes, llmNote)
	}
	if len(cutSteps) > 0 {
		notes = append(notes, "stopped at the deadline (resumed next run): "+strings.Join(cutSteps, ", "))
	}
	if llmNote != "" {
		log.Warn().Int("outages", gate.outages).Dur("waited", gate.waited.Round(time.Second)).
			Msg("Daily pipeline: waited for the LLM during the run")
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
		if err := notifier.Send(ctx, notification(status, opts.Smoke, elapsed, errParts, notes, stats, auditResult)); err != nil {
			log.Error().Err(err).Msg("Failed to send pipeline Telegram notification")
		}
	}

	// Persist a durable per-run summary for Grafana (Postgres datasource). The
	// cron process is ephemeral so Prometheus cannot scrape it; this row is
	// the source of truth for pipeline-run history. Best-effort — a write
	// failure is logged but never fails the run.
	if !opts.Smoke {
		// The LLM wait and the deadline cuts are notes, not errors: they do
		// not count in StepErrors or change the status.
		summaryLines := append(errParts, notes...)
		summary := storage.PipelineRunSummary{
			StartedAt:    start,
			FinishedAt:   time.Now(),
			Status:       status,
			StepErrors:   len(errs),
			ErrorSummary: strings.Join(summaryLines, "\n"),
			// The row's columns keep their historical meaning for the Grafana
			// dashboards: the queue size, the news plugin's parsed articles and
			// its scrape errors, not the sums over every plugin.
			Targets:      len(targets),
			NewsArticles: ingest.Result.ByPlugin["news"].Items,
			ScrapeErrors: ingest.Result.ByPlugin["news"].Errors,
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
	maxPersons int  // persons per sweep (merge, detection, regroup); 0 = all
	rejudge    int  // stale negative verdicts re-opened; 0 = skip
	audit      bool // run the auditor when due
	translate  int  // translations filled
	refresh    int  // stances refreshed
	history    bool // read the history-reading plugins one month further back
	backfill   bool // run the deadline-bound backfill phase
}

func budgets(opts RunOptions) runBudgets {
	if opts.Smoke {
		return runBudgets{maxTargets: 1, maxItems: 1, maxPersons: 1, translate: 5, refresh: 1}
	}
	return runBudgets{
		rejudge:   pairRejudgeNightlyBudget,
		audit:     true,
		translate: translateNightlyLimit,
		refresh:   stanceRefreshNightlyBudget,
		history:   true,
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
// first-time backlog drains over several runs. It returns the translations
// filled, even along with an error.
func runTranslate(ctx context.Context, storer *storage.PostgresStorer, limit int) (int, error) {
	tr, err := translate.NewOpenAITranslatorFromEnv()
	if err != nil {
		return 0, fmt.Errorf("translator unavailable: %w", err)
	}
	stats, err := translate.Fill(ctx, storer, tr, translate.FillOptions{Limit: limit})
	return stats.Filled, err
}

// maxQuickRefails caps how many times in a row a phase is run again after an
// outage the LLM "recovered" from on the first poll: an endpoint that answers
// every probe while the phase keeps failing as unavailable is one request
// failing (a completion Cloudflare cuts at 100s, a prompt the server 500s
// on), and each retry costs the interrupted item again (a news target's
// search) until the deadline.
const maxQuickRefails = 2

// errRefails marks a phase given up because it kept failing as unavailable
// while the LLM answered (maxQuickRefails). It is a step error of the phase,
// not a stop of the run.
var errRefails = errors.New("the phase keeps failing as unavailable while the LLM answers")

// runGate decides whether a phase may start (not after the run deadline, not
// once the run stopped on an LLM outage) and runs the LLM phases through the
// outage wait.
type runGate struct {
	stoppedAfter string    // the phase the run stopped at; "" while it goes on
	deadline     time.Time // zero: no deadline, and no wait
	// waitLLM blocks until the endpoint of cause (an error that wraps
	// llm.ErrUnavailable) can serve again or until passes, and reports
	// whether it came back and after how many probes (llm.WaitForEndpoint).
	// nil: never wait (smoke run).
	waitLLM func(ctx context.Context, cause error, until time.Time) (back bool, polls int)
	outages int           // outages waited for
	waited  time.Duration // total time spent waiting
}

// run runs phase through retry when the gate is open; it returns the phase's
// error, nil when the phase did not start.
func (g *runGate) run(ctx context.Context, phase string, fn func() error) error {
	if !g.open(phase) {
		return nil
	}
	return g.retry(ctx, phase, fn)
}

// retry runs fn, and while it fails because the LLM is unavailable waits for
// the LLM until the run deadline and runs fn again: every phase derives its
// pending work from the database, so the retry resumes where the outage
// stopped it. When the LLM is not back before the deadline (or the gate
// cannot wait) the run stops at this phase and fn's error is returned. When
// fn keeps failing although the LLM answered at once (maxQuickRefails), the
// phase is given up: its error, wrapping errRefails, is returned and the run
// goes on.
func (g *runGate) retry(ctx context.Context, phase string, fn func() error) error {
	quick := 0
	for {
		err := fn()
		if !errors.Is(err, llm.ErrUnavailable) {
			return err
		}
		back, polls := g.wait(ctx, phase, err)
		if back && !g.deadline.IsZero() && time.Now().After(g.deadline) {
			log.Warn().Str("phase", phase).Time("deadline", g.deadline).
				Msg("Daily pipeline: LLM back after the run deadline; phase not resumed")
			back = false
		}
		if !back {
			if ctx.Err() != nil {
				// The phase's own budget ran out during the wait: a cut of
				// this phase, not an outage that stops the run.
				return fmt.Errorf("%s stopped while waiting for the LLM: %w", phase, ctx.Err())
			}
			g.stoppedAfter = phase
			return err
		}
		if polls > 1 {
			quick = 0
			continue
		}
		if quick++; quick > maxQuickRefails {
			log.Error().Err(err).Str("phase", phase).Int("retries", maxQuickRefails).
				Msg("Daily pipeline: the LLM answers but the phase keeps failing as unavailable; giving it up")
			return fmt.Errorf("%w: %w", errRefails, err)
		}
	}
}

// maxLLMWait is the total time a run waits for the LLM over all its outages.
// After it, the run stops at the phase and the next run resumes the work: an
// endpoint that is down for good must not keep the cron container up until
// the run deadline. A Claude seat at its usage limit that resets after the
// end of the budget (for example 3 h later) also stops the run at the phase:
// this is the intended behavior, and the next night resumes the work.
const maxLLMWait = 30 * time.Minute

// wait waits for the LLM until the run deadline or the end of maxLLMWait,
// whichever comes first; back is false when the gate cannot wait, the wait
// budget is spent, or the LLM did not come back.
func (g *runGate) wait(ctx context.Context, phase string, cause error) (back bool, polls int) {
	if g.waitLLM == nil || g.deadline.IsZero() {
		return false, 0
	}
	left := maxLLMWait - g.waited
	if left <= 0 {
		log.Error().Err(cause).Str("phase", phase).Dur("waited", g.waited.Round(time.Second)).
			Msg("Daily pipeline: LLM unavailable and the wait budget is spent; stopping the run")
		return false, 0
	}
	until := g.deadline
	if budgetEnd := time.Now().Add(left); budgetEnd.Before(until) {
		until = budgetEnd
	}
	g.outages++
	log.Warn().Err(cause).Str("phase", phase).Int("outage", g.outages).Time("until", until).
		Msg("Daily pipeline: LLM unavailable; waiting for it to resume the phase")
	start := time.Now()
	back, polls = g.waitLLM(ctx, cause, until)
	g.waited += time.Since(start)
	if back {
		log.Info().Str("phase", phase).Dur("waited", time.Since(start).Round(time.Second)).Int("polls", polls).
			Msg("Daily pipeline: LLM back; resuming the phase")
	}
	return back, polls
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

// note summarizes the outages the run waited for; "" when none.
func (g *runGate) note() string {
	if g.outages == 0 {
		return ""
	}
	return fmt.Sprintf("LLM outages waited for: %d (%s)", g.outages, g.waited.Round(time.Second))
}

// mergeAuditTries folds the result of an audit try into the earlier tries'
// (nil when none). A retried audit lists again every row but those the
// earlier tries deleted, and re-judges or pins them, so the last try's counts
// stand, plus the earlier deletions.
func mergeAuditTries(prev, next *contradiction.AuditRunResult) *contradiction.AuditRunResult {
	if prev == nil {
		return next
	}
	if next == nil {
		return prev
	}
	merged := *next
	merged.Total += prev.Deleted
	merged.Remove += prev.Deleted
	merged.Deleted += prev.Deleted
	merged.Elapsed += prev.Elapsed
	return &merged
}

// budgeted adapts a phase bounded by a per-run budget to runGate.retry: each
// try gets what the previous tries left (run reports what it used, even along
// with an error), and no try starts once the budget is spent.
func budgeted(budget int, run func(left int) (used int, err error)) func() error {
	used := 0
	return func() error {
		left := budget - used
		if left <= 0 {
			return nil
		}
		n, err := run(left)
		used += n
		return err
	}
}

// splitCut splits err into the deadline cut it carries and the rest: the
// parts (walking errors.Join trees and %w wrappers) that are ctx's own expiry,
// and the other failures, nil when none. Without ctx expired nothing is a
// cut: a client's own timeout also matches context.DeadlineExceeded.
func splitCut(ctx context.Context, err error) (cut bool, rest error) {
	if err == nil || ctx.Err() == nil {
		return false, err
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var kept []error
		for _, e := range joined.Unwrap() {
			c, r := splitCut(ctx, e)
			cut = cut || c
			if r != nil {
				kept = append(kept, r)
			}
		}
		return cut, errors.Join(kept...)
	}
	if inner := errors.Unwrap(err); inner != nil {
		c, r := splitCut(ctx, inner)
		switch {
		case !c:
			return false, err
		case r == nil:
			return true, nil
		}
		// Keep the wrapper's context ("ingest twitter: …") on what remains.
		prefix := strings.TrimSuffix(err.Error(), inner.Error())
		return true, fmt.Errorf("%s%w", prefix, r)
	}
	if errors.Is(err, ctx.Err()) {
		return true, nil
	}
	return false, err
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

func notification(status string, smoke bool, elapsed time.Duration, errParts []string, notes []string, s plugins.IngestStats, audit *contradiction.AuditRunResult) string {
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
	for _, n := range notes {
		msg += "\n⏸ " + n
	}
	if audit != nil && audit.Total > 0 {
		msg += fmt.Sprintf("\n🔍 audit: %d checked, %d evicted (2-of-2 FP), %d kept (unstable)", audit.Total, audit.Deleted, audit.Unstable)
	}
	return msg
}
