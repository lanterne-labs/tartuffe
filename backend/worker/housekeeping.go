package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/pipeline"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/translate"

	"github.com/rs/zerolog/log"
)

// The caps of one maintenance pass. The pending work carries over to the
// next pass: every step derives its pending set from the database.
const (
	// rejudgeBudget caps how many stale negative pair verdicts are re-opened
	// per pass after a rulebook change.
	rejudgeBudget = 4500
	// translateLimit caps how many translations a pass fills, so a large
	// first-time backlog drains over several passes.
	translateLimit = 4500
	// refreshBudget caps how many stances a pass brings up to date (one LLM
	// call each).
	refreshBudget = 2000
	// auditCadence is how often the contradiction auditor re-judges the
	// stored contradictions.
	auditCadence = 5 * 24 * time.Hour

	// maintenanceJob and feedJob are the job_metrics rows that remember the
	// last pass and the last feed.
	maintenanceJob = "maintenance"
	feedJob        = "feed"
)

// Housekeeping is what the worker does when no task is pending.
//
// The feed runs once a day, after FeedHourUTC: the ingest of the plugins that
// read their own source (frpolitics: the votes and the transcripts of the
// day), the backfills, bounded by BackfillCap, and one task per watched person
// (the search queue) and period plugin for the previous day.
//
// The maintenance pass brings the dataset up to date after new facts: the
// stance refresh, the stance merge sweep, the contradiction detection over the
// persons with new facts, the duplicate repoint, the regroup of the persons
// with new contradictions, the audit when due, and the translations. It runs
// when facts were added since the last pass, and at least every
// MaintenanceEvery (the audit cadence, a backlog cut by a cap).
type Housekeeping struct {
	Storer *storage.PostgresStorer
	Deps   *deps.Deps
	// MaintenanceEvery is the longest time between two passes.
	MaintenanceEvery time.Duration
	// FeedHourUTC is the hour of the day after which the feed runs.
	FeedHourUTC int
	// BackfillCap bounds the backfills of one feed.
	BackfillCap time.Duration
	// Now is the clock (nil: time.Now).
	Now func() time.Time

	dirty bool
}

// DefaultHousekeeping are the settings of `dm worker run`.
var DefaultHousekeeping = Housekeeping{
	MaintenanceEvery: 6 * time.Hour,
	// The AN archive is refreshed around 02:05 UTC.
	FeedHourUTC: 3,
	BackfillCap: time.Hour,
}

func (h *Housekeeping) now() time.Time {
	if h.Now != nil {
		return h.Now()
	}
	return time.Now()
}

// FactsAdded implements Idle: a task added facts, so the next idle moment
// runs a maintenance pass.
func (h *Housekeeping) FactsAdded(n int) {
	if n > 0 {
		h.dirty = true
	}
}

// Idle implements Idle: the feed when due, then a maintenance pass when due.
// An unavailable LLM stops the work with an error wrapping llm.ErrUnavailable;
// the other failures are joined and returned after the work went on.
func (h *Housekeeping) Idle(ctx context.Context) error {
	var errs []error
	due, err := h.feedDue(ctx)
	if err != nil {
		errs = append(errs, err)
	} else if due {
		if err := h.feed(ctx); err != nil {
			if errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil {
				return err
			}
			errs = append(errs, err)
		}
	}
	due, since, err := h.maintenanceDue(ctx)
	if err != nil {
		errs = append(errs, err)
	} else if due {
		if err := h.maintenance(ctx, since); err != nil {
			if errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil {
				return err
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// feedDue reports whether the feed of the day has not run yet: the last feed
// is before the last FeedHourUTC that passed.
func (h *Housekeeping) feedDue(ctx context.Context) (bool, error) {
	last, err := h.Storer.GetLatestJobRunTime(ctx, feedJob)
	if err != nil {
		return false, err
	}
	now := h.now().UTC()
	at := time.Date(now.Year(), now.Month(), now.Day(), h.FeedHourUTC, 0, 0, 0, time.UTC)
	if at.After(now) {
		at = at.AddDate(0, 0, -1)
	}
	return last == nil || last.Before(at), nil
}

// feedPlugins lists the registered plugins that read their own source: the
// ingesters that do not read by period.
func feedPlugins() []string {
	var out []string
	for _, p := range pipeline.Plugins {
		_, ingests := p.(plugins.Ingester)
		_, byPeriod := p.(plugins.HistoryReader)
		if ingests && !byPeriod {
			out = append(out, p.Name())
		}
	}
	return out
}

// feed runs the feed of the day.
func (h *Housekeeping) feed(ctx context.Context) error {
	start := h.now()
	log.Info().Strs("plugins", feedPlugins()).Msg("Feed started")
	var errs []error

	var in pipeline.IngestRun
	var err error
	if names := feedPlugins(); len(names) > 0 {
		// An empty list would mean every plugin to IngestRun.
		err = in.Run(ctx, h.Deps, nil, plugins.IngestOptions{}, names)
	}
	if errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil {
		return fmt.Errorf("feed: %w", err)
	}
	for name, e := range in.Errs {
		errs = append(errs, fmt.Errorf("feed %s: %w", name, e))
	}
	h.FactsAdded(in.Result.Total.Facts)

	// The cap is a deadline of the context too: a backfill that does not read
	// the deadline it is given (photos) stops with the context.
	deadline := h.now().Add(h.BackfillCap)
	bctx, cancel := context.WithDeadline(ctx, deadline)
	var bf pipeline.BackfillRun
	err = bf.Run(bctx, h.Deps, deadline)
	cancel()
	if errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil {
		return fmt.Errorf("backfill: %w", err)
	}
	for name, e := range bf.Errs {
		if errors.Is(e, context.DeadlineExceeded) && ctx.Err() == nil {
			log.Info().Str("plugin", name).Msg("Feed: backfill stopped at its cap; the next feed goes on")
			continue
		}
		errs = append(errs, fmt.Errorf("backfill %s: %w", name, e))
	}

	added, err := h.watchTasks(ctx)
	if err != nil {
		errs = append(errs, err)
	}

	if err := h.Storer.RecordJobMetrics(ctx, []storage.JobMetric{
		{JobType: feedJob, Metric: "facts", Value: float64(in.Result.Total.Facts)},
		{JobType: feedJob, Metric: "watch_tasks", Value: float64(added)},
	}); err != nil {
		errs = append(errs, fmt.Errorf("recording the feed: %w", err))
	}
	log.Info().Dur("elapsed", h.now().Sub(start).Round(time.Second)).Int("facts", in.Result.Total.Facts).
		Int("watch_tasks", added).Int("errors", len(errs)).Msg("Feed done")
	llm.LogUsage()
	return errors.Join(errs...)
}

// watchTasks adds, for every watched person and period plugin, the task of
// the previous day. Returns how many tasks were new.
func (h *Housekeeping) watchTasks(ctx context.Context) (int, error) {
	targets, err := pipeline.QueueTargets(ctx, h.Storer)
	if err != nil {
		return 0, fmt.Errorf("watch list: %w", err)
	}
	until := Day(h.now())
	since := until.AddDate(0, 0, -1)
	var tasks []storage.NewCollectionTask
	for _, t := range targets {
		for _, name := range PeriodPlugins() {
			tasks = append(tasks, storage.NewCollectionTask{PersonID: t.PersonID, Plugin: name, Since: since, Until: until, RequestedBy: "watch"})
		}
	}
	added, err := h.Storer.AddCollectionTasks(ctx, tasks)
	if err != nil {
		return added, fmt.Errorf("watch list: %w", err)
	}
	return added, nil
}

// maintenanceDue reports whether a pass is due, and the start of the last
// pass (zero when none ran): the pass judges what changed after it.
func (h *Housekeeping) maintenanceDue(ctx context.Context) (bool, time.Time, error) {
	last, since, err := h.lastMaintenance(ctx)
	if err != nil {
		return false, time.Time{}, err
	}
	due := h.dirty || last.IsZero() || h.now().Sub(last) >= h.MaintenanceEvery
	return due, since, nil
}

// lastMaintenance returns the end and the start of the last pass (zero when
// none ran). The pass records its duration, so its start is the end minus it.
func (h *Housekeeping) lastMaintenance(ctx context.Context) (end, start time.Time, err error) {
	at, seconds, ok, err := h.Storer.LastJobMetric(ctx, maintenanceJob, "duration_seconds")
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("last maintenance pass: %w", err)
	}
	if !ok {
		return time.Time{}, time.Time{}, nil
	}
	return at, at.Add(-time.Duration(seconds * float64(time.Second))), nil
}

// maintenance runs one pass over what changed after since (everything when
// since is zero).
func (h *Housekeeping) maintenance(ctx context.Context, since time.Time) error {
	start := h.now()
	log.Info().Time("since", since).Bool("facts_added", h.dirty).Msg("Maintenance pass started")
	var errs []error
	step := func(name string, err error) error {
		if err == nil {
			return nil
		}
		if errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil {
			return fmt.Errorf("maintenance %s: %w", name, err)
		}
		errs = append(errs, fmt.Errorf("%s: %w", name, err))
		return nil
	}
	storer := h.Storer

	sm, err := h.Deps.StanceManager()
	if err != nil {
		return fmt.Errorf("maintenance: stance manager: %w", err)
	}
	// 1. The label and summary of each stance with pending facts.
	_, err = core.RefreshPendingStances(ctx, storer, sm, refreshBudget)
	if err := step("stance refresh", err); err != nil {
		return err
	}
	// 2. The stances of one person that cover one subject are merged; the
	// facts they held are judged against each other.
	_, err = core.RunStanceMergeSweep(ctx, storer, sm, core.DefaultStanceMergeOptions)
	if err := step("stance merge", err); err != nil {
		return err
	}
	// 3. Detection across stances. A NO verdict is final only under the
	// rulebook that produced it: the oldest stale negatives are re-opened
	// first. Then the plugins queue the structural pairs the detector cannot
	// see, and the detector judges the persons with new facts (every person
	// after a rulebook change or on the first pass).
	deleted, remaining, err := storer.EvictStaleJudgedPairs(ctx, contradiction.JudgeRulebookHash(), rejudgeBudget)
	if err := step("stale verdict eviction", err); err != nil {
		return err
	}
	if deleted > 0 || remaining > 0 {
		log.Info().Int("reopened", deleted).Int("stale_remaining", remaining).Msg("Maintenance: re-opened stale negative pair verdicts")
	}
	if _, pErrs := pipeline.ProducePairs(ctx, h.Deps); len(pErrs) > 0 {
		for name, e := range pErrs {
			if err := step("pairs "+name, e); err != nil {
				return err
			}
		}
	}
	var persons []storage.PersonInfo
	all := since.IsZero() || deleted > 0
	if !all {
		persons, err = storer.PersonsWithFactsSince(ctx, since)
		if err := step("persons with new facts", err); err != nil {
			return err
		}
	}
	if all || len(persons) > 0 {
		_, err = contradiction.RunDetection(ctx, storer, contradiction.DetectOptions{Persons: persons})
		if err := step("detection", err); err != nil {
			return err
		}
	}
	// 4. Contradictions move off the facts the detection marked as duplicates.
	if processed, repointed, dropped, err := storer.RepointDuplicates(ctx, true); err != nil {
		errs = append(errs, fmt.Errorf("repoint duplicates: %w", err))
	} else if processed > 0 {
		log.Info().Int("duplicates", processed).Int("repointed", repointed).Int("dropped", dropped).Msg("Maintenance: repointed contradictions off duplicate facts")
	}
	// 5. The contradiction groups of the persons with new contradictions.
	var ids []int64
	if !all {
		ids, err = storer.PersonIDsWithContradictionsSince(ctx, since)
		if err := step("persons with new contradictions", err); err != nil {
			return err
		}
	}
	if all || len(ids) > 0 {
		if err := step("regroup", contradiction.RunRegroupStep(ctx, storer, ids)); err != nil {
			return err
		}
	}
	// 6. The audit of the stored contradictions, every auditCadence.
	if err := step("audit", h.auditIfDue(ctx)); err != nil {
		return err
	}
	// 7. The missing translations.
	if err := step("translate", h.translate(ctx)); err != nil {
		return err
	}

	h.dirty = false
	elapsed := h.now().Sub(start)
	if err := storer.RecordJobMetrics(ctx, []storage.JobMetric{{JobType: maintenanceJob, Metric: "duration_seconds", Value: elapsed.Seconds()}}); err != nil {
		errs = append(errs, fmt.Errorf("recording the pass: %w", err))
	}
	log.Info().Dur("elapsed", elapsed.Round(time.Second)).Int("persons_detected", len(persons)).Int("persons_regrouped", len(ids)).
		Int("errors", len(errs)).Msg("Maintenance pass done")
	llm.LogUsage()
	return errors.Join(errs...)
}

// auditIfDue runs the contradiction auditor at most once per auditCadence.
// An audit that judged its rows records a job_metrics row; a failed audit
// records none, so the next pass tries again.
func (h *Housekeeping) auditIfDue(ctx context.Context) error {
	last, err := h.Storer.GetLatestJobRunTime(ctx, "contradiction-audit")
	if err != nil {
		return fmt.Errorf("last audit: %w", err)
	}
	if last != nil && h.now().Sub(*last) < auditCadence {
		return nil
	}
	log.Info().Dur("cadence", auditCadence).Msg("Maintenance: contradiction audit due")
	res, err := contradiction.RunAudit(ctx, h.Storer, contradiction.AuditOptions{
		Scope:        "all persons (periodic)",
		RecordMetric: true,
		// A verdict=remove confirmed by the independent passes is deleted, so
		// false positives never accumulate between passes.
		Apply: true,
	})
	if err != nil {
		return err
	}
	if res != nil && res.Total > 0 {
		log.Info().Int("checked", res.Total).Int("evicted", res.Deleted).Int("unstable", res.Unstable).Msg("Maintenance: contradiction audit done")
	}
	return nil
}

// translate fills the missing translations, translateLimit at most.
func (h *Housekeeping) translate(ctx context.Context) error {
	tr, err := translate.NewOpenAITranslatorFromEnv()
	if err != nil {
		return fmt.Errorf("translator unavailable: %w", err)
	}
	stats, err := translate.Fill(ctx, h.Storer, tr, translate.FillOptions{Limit: translateLimit})
	if stats.Filled > 0 {
		log.Info().Int("filled", stats.Filled).Msg("Maintenance: translations filled")
	}
	return err
}
