// Package pipeline drives the registered plugins through the phases of a run.
// It is the only place the concrete plugins are named (see registry.go);
// callers pick phases and, optionally, a subset of plugins by name.
package pipeline

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend/deps"
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

// Ingest runs the ingest phase of every selected plugin that has one and
// returns the aggregated stats plus the per-plugin errors.
func Ingest(ctx context.Context, d *deps.Deps, targets []plugins.Target, opts plugins.IngestOptions, only []string) (plugins.IngestStats, StepErrors, error) {
	sel, err := selected(only)
	if err != nil {
		return plugins.IngestStats{}, nil, err
	}
	var total plugins.IngestStats
	errs := StepErrors{}
	for _, p := range sel {
		ing, ok := p.(plugins.Ingester)
		if !ok {
			continue
		}
		start := time.Now()
		stats, err := ing.Ingest(ctx, d, targets, opts)
		total.Add(stats)
		if err != nil {
			errs[p.Name()] = err
			log.Error().Err(err).Str("plugin", p.Name()).Msg("Pipeline: ingest failed")
		}
		log.Info().Str("plugin", p.Name()).Dur("elapsed", time.Since(start)).
			Int("targets", stats.Targets).Int("items", stats.Items).Int("facts", stats.Facts).Int("errors", stats.Errors).
			Msg("Pipeline: ingest done")
	}
	return total, errs, nil
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

// Backfill runs the backfill phase of every plugin that has one, in
// registration order, each bounded by the same deadline.
func Backfill(ctx context.Context, d *deps.Deps, deadline time.Time) StepErrors {
	errs := StepErrors{}
	for _, p := range Plugins {
		bf, ok := p.(plugins.Backfiller)
		if !ok {
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
