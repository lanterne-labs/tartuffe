// Package pipeline drives the registered plugins through the phases of a run.
// It is the only place the concrete plugins are named (see registry.go);
// callers pick phases and, optionally, a subset of plugins by name.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// StepErrors maps a plugin name to the error its phase returned.
type StepErrors map[string]error

// selected returns the registered plugins, restricted to names when non-empty.
// An unknown name is an error so a typo never silently runs nothing.
func selected(names []string) ([]plugins.Plugin, error) {
	if len(names) == 0 {
		return Plugins, nil
	}
	byName := make(map[string]plugins.Plugin, len(Plugins))
	for _, p := range Plugins {
		byName[p.Name()] = p
	}
	out := make([]plugins.Plugin, 0, len(names))
	for _, n := range names {
		p, ok := byName[n]
		if !ok {
			return nil, fmt.Errorf("unknown plugin %q (registered: %v)", n, Names())
		}
		out = append(out, p)
	}
	return out, nil
}

// QueueTargets converts the search_queue contents (the watch list) into
// plugin targets.
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

// Find returns the registered plugin of that name.
func Find(name string) (plugins.Plugin, error) {
	sel, err := selected([]string{name})
	if err != nil {
		return nil, err
	}
	return sel[0], nil
}

// Names lists the registered plugin names in run order.
func Names() []string {
	out := make([]string, 0, len(Plugins))
	for _, p := range Plugins {
		out = append(out, p.Name())
	}
	return out
}

// IngestResult is the outcome of the ingest phase: the stats summed over the
// plugins that ran, and each plugin's own.
type IngestResult struct {
	Total    plugins.IngestStats
	ByPlugin map[string]plugins.IngestStats
}

// Ingest runs the ingest phase of every selected plugin that has one and
// returns the stats plus the per-plugin errors. Once a plugin fails because
// the LLM is unavailable (llm.ErrUnavailable) the remaining plugins are not
// started: their items would fail the same way.
func Ingest(ctx context.Context, d *deps.Deps, targets []plugins.Target, opts plugins.IngestOptions, only []string) (IngestResult, StepErrors, error) {
	var in IngestRun
	if err := in.Run(ctx, d, targets, opts, only); err != nil && !errors.Is(err, llm.ErrUnavailable) {
		return IngestResult{}, nil, err
	}
	return in.Result, in.Errs, nil
}

// IngestRun is an ingest phase that resumes after an LLM outage: calling Run
// again skips the plugins that finished and runs the interrupted one on the
// targets it did not report done (plugins.IngestStats.Done), extracting only
// the stored items of those it had fetched (Fetched → ExtractOnly), with what
// is left of its own per-run cap (Spent).
type IngestRun struct {
	Result      IngestResult
	Errs        StepErrors
	finished    map[string]bool
	interrupted string // the plugin the last Run stopped at; "" when none
}

// Run runs the ingest phase of every selected plugin not finished yet, and
// accumulates the stats and per-plugin errors. It stops at the first plugin
// the LLM became unavailable for and returns that plugin's error (wrapping
// llm.ErrUnavailable, also kept in Errs until a later Run retries the
// plugin, which keeps only the other failures of the interrupted call);
// other plugin failures are only recorded in Errs. Once ctx is done no
// further plugin starts and the returned error wraps ctx.Err().
func (in *IngestRun) Run(ctx context.Context, d *deps.Deps, targets []plugins.Target, opts plugins.IngestOptions, only []string) error {
	sel, err := selected(only)
	if err != nil {
		return err
	}
	if in.finished == nil {
		in.Result.ByPlugin = map[string]plugins.IngestStats{}
		in.Errs = StepErrors{}
		in.finished = map[string]bool{}
	}
	for _, p := range sel {
		name := p.Name()
		ing, ok := p.(plugins.Ingester)
		if !ok || in.finished[name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("ingest stopped before %s: %w", name, err)
		}
		prev := in.Result.ByPlugin[name]
		left := targetsLeft(targets, prev.Done)
		callOpts := opts
		callOpts.ExtractOnly = idsLeft(prev.Fetched, prev.Done)
		callOpts.Spent = prev.Spent
		if in.interrupted == name {
			log.Info().Str("plugin", name).Int("targets_left", len(left)).Int("targets_done", len(targets)-len(left)).
				Int("extract_only", len(callOpts.ExtractOnly)).Int("spent", callOpts.Spent).
				Msg("Pipeline: resuming ingest after the LLM outage")
		}
		start := time.Now()
		stats, err := ing.Ingest(ctx, d, left, callOpts)
		in.Result.Total.Add(stats)
		prev.Add(stats)
		in.Result.ByPlugin[name] = prev
		if err != nil {
			log.Error().Err(err).Str("plugin", name).Msg("Pipeline: ingest failed")
		}
		setStepErr(in.Errs, name, in.interrupted == name, err)
		log.Info().Str("plugin", name).Dur("elapsed", time.Since(start)).
			Int("targets", stats.Targets).Int("items", stats.Items).Int("facts", stats.Facts).Int("errors", stats.Errors).
			Msg("Pipeline: ingest done")
		if errors.Is(err, llm.ErrUnavailable) {
			in.interrupted = name
			return fmt.Errorf("ingest %s: %w", name, err)
		}
		in.interrupted = ""
		in.finished[name] = true
	}
	return nil
}

// Abandon gives up the plugin the last Run stopped at: it counts as finished,
// its error stays in Errs, and the next Run goes on with the other plugins.
// It reports whether there was such a plugin.
func (in *IngestRun) Abandon() bool {
	if in.interrupted == "" {
		return false
	}
	in.finished[in.interrupted] = true
	in.interrupted = ""
	return true
}

// idsLeft is ids without those in done.
func idsLeft(ids, done []int64) []int64 {
	skip := make(map[int64]bool, len(done))
	for _, id := range done {
		skip[id] = true
	}
	var out []int64
	for _, id := range ids {
		if !skip[id] {
			out = append(out, id)
		}
	}
	return out
}

// setStepErr records a plugin call's error in errs. On the retry of an
// interrupted plugin the earlier call's failures are kept, but not its
// outage, which the retry replaces.
func setStepErr(errs StepErrors, name string, retry bool, err error) {
	var prev error
	if retry {
		prev = withoutOutage(errs[name])
	}
	if joined := errors.Join(prev, err); joined != nil {
		errs[name] = joined
		return
	}
	delete(errs, name)
}

// withoutOutage is err without its parts that wrap llm.ErrUnavailable
// (walking errors.Join trees); nil when nothing else is left.
func withoutOutage(err error) error {
	if err == nil {
		return nil
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var kept []error
		for _, e := range joined.Unwrap() {
			if k := withoutOutage(e); k != nil {
				kept = append(kept, k)
			}
		}
		return errors.Join(kept...)
	}
	if errors.Is(err, llm.ErrUnavailable) {
		return nil
	}
	return err
}

// targetsLeft is targets without the persons in done.
func targetsLeft(targets []plugins.Target, done []int64) []plugins.Target {
	if len(done) == 0 {
		return targets
	}
	skip := make(map[int64]bool, len(done))
	for _, id := range done {
		skip[id] = true
	}
	out := make([]plugins.Target, 0, len(targets))
	for _, t := range targets {
		if !skip[t.PersonID] {
			out = append(out, t)
		}
	}
	return out
}

// ProducePairs runs the pair-producing phase of every plugin that has one and
// returns the total pairs queued plus the per-plugin errors. Once ctx is done
// no further plugin starts: the first one not started gets an error wrapping
// ctx.Err().
func ProducePairs(ctx context.Context, d *deps.Deps) (int, StepErrors) {
	total := 0
	errs := StepErrors{}
	for _, p := range Plugins {
		pp, ok := p.(plugins.PairProducer)
		if !ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			errs[p.Name()] = fmt.Errorf("pair production stopped: %w", err)
			break
		}
		n, err := pp.ProducePairs(ctx, d)
		if err != nil {
			errs[p.Name()] = err
			log.Error().Err(err).Str("plugin", p.Name()).Msg("Pipeline: pair production failed")
			continue
		}
		total += n
		if n > 0 {
			log.Info().Str("plugin", p.Name()).Int("queued", n).Msg("Pipeline: fact pairs queued for detection")
		}
	}
	return total, errs
}

// BackfillRun is a backfill phase that resumes after an LLM outage: calling
// Run again skips the plugins that finished.
type BackfillRun struct {
	Errs        StepErrors
	finished    map[string]bool
	interrupted string // the plugin the last Run stopped at; "" when none
}

// Run runs the backfill phase of every plugin that has one and did not finish
// yet, in registration order, each bounded by the same deadline; a plugin
// reached after the deadline is not started. It stops at the first plugin the
// LLM became unavailable for and returns that plugin's error (wrapping
// llm.ErrUnavailable, also kept in Errs until a later Run retries the
// plugin); other plugin failures are only recorded in Errs. Once ctx is done
// no further plugin starts: the first one not started gets an error wrapping
// ctx.Err() in Errs, which is also returned.
func (bf *BackfillRun) Run(ctx context.Context, d *deps.Deps, deadline time.Time) error {
	if bf.finished == nil {
		bf.Errs = StepErrors{}
		bf.finished = map[string]bool{}
	}
	for _, p := range Plugins {
		name := p.Name()
		b, ok := p.(plugins.Backfiller)
		if !ok || bf.finished[name] {
			continue
		}
		if err := ctx.Err(); err != nil {
			stop := fmt.Errorf("backfill stopped before %s: %w", name, err)
			setStepErr(bf.Errs, name, false, stop)
			return stop
		}
		if time.Now().After(deadline) {
			log.Info().Str("plugin", name).Time("deadline", deadline).Msg("Pipeline: backfill window closed; skipping")
			continue
		}
		if bf.interrupted == name {
			log.Info().Str("plugin", name).Msg("Pipeline: resuming backfill after the LLM outage")
		}
		start := time.Now()
		err := b.Backfill(ctx, d, deadline)
		if err != nil {
			log.Error().Err(err).Str("plugin", name).Msg("Pipeline: backfill failed")
		}
		setStepErr(bf.Errs, name, bf.interrupted == name, err)
		log.Info().Str("plugin", name).Dur("elapsed", time.Since(start)).Msg("Pipeline: backfill done")
		if errors.Is(err, llm.ErrUnavailable) {
			bf.interrupted = name
			return fmt.Errorf("backfill %s: %w", name, err)
		}
		bf.interrupted = ""
		bf.finished[name] = true
	}
	return nil
}

// Abandon gives up the plugin the last Run stopped at, like
// IngestRun.Abandon.
func (bf *BackfillRun) Abandon() bool {
	if bf.interrupted == "" {
		return false
	}
	bf.finished[bf.interrupted] = true
	bf.interrupted = ""
	return true
}
