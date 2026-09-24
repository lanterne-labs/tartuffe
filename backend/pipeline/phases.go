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
	sel, err := selected(only)
	if err != nil {
		return IngestResult{}, nil, err
	}
	res := IngestResult{ByPlugin: map[string]plugins.IngestStats{}}
	errs := StepErrors{}
	for _, p := range sel {
		ing, ok := p.(plugins.Ingester)
		if !ok {
			continue
		}
		if llmDown(errs) {
			log.Warn().Str("plugin", p.Name()).Msg("Pipeline: LLM unavailable; ingest not started")
			continue
		}
		start := time.Now()
		stats, err := ing.Ingest(ctx, d, targets, opts)
		res.Total.Add(stats)
		res.ByPlugin[p.Name()] = stats
		if err != nil {
			errs[p.Name()] = err
			log.Error().Err(err).Str("plugin", p.Name()).Msg("Pipeline: ingest failed")
		}
		log.Info().Str("plugin", p.Name()).Dur("elapsed", time.Since(start)).
			Int("targets", stats.Targets).Int("items", stats.Items).Int("facts", stats.Facts).Int("errors", stats.Errors).
			Msg("Pipeline: ingest done")
	}
	return res, errs, nil
}

// ProducePairs runs the pair-producing phase of every plugin that has one and
// returns the total pairs queued plus the per-plugin errors.
func ProducePairs(ctx context.Context, d *deps.Deps) (int, StepErrors) {
	total := 0
	errs := StepErrors{}
	for _, p := range Plugins {
		pp, ok := p.(plugins.PairProducer)
		if !ok {
			continue
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

// Backfill runs the backfill phase of every selected plugin that has one, in
// registration order, each bounded by the same deadline. Like Ingest, it
// starts no further plugin once one failed because the LLM is unavailable.
func Backfill(ctx context.Context, d *deps.Deps, deadline time.Time, only []string) StepErrors {
	sel, err := selected(only)
	if err != nil {
		return StepErrors{"backfill": err}
	}
	errs := StepErrors{}
	for _, p := range sel {
		bf, ok := p.(plugins.Backfiller)
		if !ok {
			continue
		}
		if llmDown(errs) {
			log.Warn().Str("plugin", p.Name()).Msg("Pipeline: LLM unavailable; backfill not started")
			continue
		}
		if time.Now().After(deadline) {
			log.Info().Str("plugin", p.Name()).Time("deadline", deadline).Msg("Pipeline: backfill window closed; skipping")
			continue
		}
		start := time.Now()
		if err := bf.Backfill(ctx, d, deadline); err != nil {
			errs[p.Name()] = err
			log.Error().Err(err).Str("plugin", p.Name()).Msg("Pipeline: backfill failed")
		}
		log.Info().Str("plugin", p.Name()).Dur("elapsed", time.Since(start)).Msg("Pipeline: backfill done")
	}
	return errs
}

// llmDown reports whether one of errs says the LLM is unavailable.
func llmDown(errs StepErrors) bool {
	for _, err := range errs {
		if errors.Is(err, llm.ErrUnavailable) {
			return true
		}
	}
	return false
}
