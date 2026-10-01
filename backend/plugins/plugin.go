// Package plugins defines the contract between the pipeline and a data source.
//
// A plugin owns everything about its source: how to fetch it, how to turn it
// into facts, which tables it keeps, which CLI it exposes. The pipeline knows
// only the phases below and drives every registered plugin through them in
// order; a plugin implements the phases it needs and nothing else.
package plugins

import (
	"context"
	"time"

	"digital-mirror/backend/deps"
)

// Target is a tracked person or party a run collects facts about.
type Target struct {
	PersonID   int64
	Name       string
	EntityType string // persons.entity_type: "human", "party", "organization"
}

// IsParty reports whether the target is a party or organization rather than a
// person.
func (t Target) IsParty() bool {
	return t.EntityType == "party" || t.EntityType == "organization"
}

// Plugin is a data source known to the pipeline.
type Plugin interface {
	Name() string
}

// IngestOptions are the generic knobs of an ingest run. Plugin-specific
// tuning lives in the plugin's own options, reachable through its own CLI.
type IngestOptions struct {
	// Since bounds the content date range from below (nil = plugin default).
	Since *time.Time
	// Until bounds the content date range from above (nil = now).
	Until *time.Time
	// WindowDays splits the date range into windows of N days (0 = one pass).
	WindowDays int
	// DryRun fetches and extracts without persisting anything.
	DryRun bool
	// Force bypasses the plugin's already-processed checks.
	Force bool
	// MaxItems caps the content units fetched per target (articles, tweets,
	// laws, transcript documents); 0 = the plugin's own default. Used by the
	// pipeline's smoke run.
	MaxItems int
	// ExtractOnly lists targets (person ids) whose fetch completed in an
	// earlier call that an LLM outage interrupted (IngestStats.Fetched): the
	// plugin only extracts their stored items, without fetching again.
	ExtractOnly []int64
	// Spent is the part of the plugin's own per-run cap (MaxItems or its
	// default) earlier calls of the same run used (IngestStats.Spent).
	Spent int
}

// IngestStats is what an ingest phase reports for the run summary.
type IngestStats struct {
	// Targets is the number of targets processed.
	Targets int
	// Items is the number of content units fetched (articles, tweets, laws,
	// transcripts).
	Items int
	// Facts is the number of facts inserted.
	Facts int
	// Errors is the number of per-item failures that did not abort the run.
	Errors int
	// Details are per-target error lines for the run notification.
	Details []string
	// Done lists the person ids of the targets this call finished, collected
	// or failed, even when it returns an error. After an LLM outage the
	// pipeline runs the plugin again on the other targets only. A plugin with
	// its own scope leaves it empty.
	Done []int64
	// Fetched lists the targets whose fetch completed but whose extraction an
	// LLM outage interrupted: a retry passes them as IngestOptions.ExtractOnly.
	Fetched []int64
	// Spent is the part of the plugin's own per-run cap this call used, even
	// when it returns an error.
	Spent int
}

// Add accumulates o into s.
func (s *IngestStats) Add(o IngestStats) {
	s.Targets += o.Targets
	s.Items += o.Items
	s.Facts += o.Facts
	s.Errors += o.Errors
	s.Details = append(s.Details, o.Details...)
	s.Done = append(s.Done, o.Done...)
	s.Fetched = append(s.Fetched, o.Fetched...)
	s.Spent += o.Spent
}

// Ingester collects new facts. targets are the run's queue-scoped persons; a
// plugin whose source has its own scope (official records) ignores them.
type Ingester interface {
	Ingest(ctx context.Context, d *deps.Deps, targets []Target, opts IngestOptions) (IngestStats, error)
}

// HistoryReader is an Ingester whose source can be read back in time by date
// range (IngestOptions.Since/Until/WindowDays). The pipeline's history step
// reads such plugins further back each run, for the queue-scoped targets.
type HistoryReader interface {
	Ingester
	// HistoryFetched reports whether everything the targets published in
	// [since, until) has been fetched and stored (from the plugin's own fetch
	// log): that stretch is never fetched again, and the step moves on.
	HistoryFetched(ctx context.Context, d *deps.Deps, targets []Target, since, until time.Time) (bool, error)
}

// TargetIDs returns the person ids of targets.
func TargetIDs(targets []Target) []int64 {
	ids := make([]int64, 0, len(targets))
	for _, t := range targets {
		ids = append(ids, t.PersonID)
	}
	return ids
}

// PairProducer queues fact pairs for the contradiction detector on grounds the
// detector cannot see (provenance, structure). Runs after stale-verdict
// eviction and before detection; returns the number of pairs queued.
type PairProducer interface {
	ProducePairs(ctx context.Context, d *deps.Deps) (int, error)
}

// Backfiller does catch-up work that must finish by deadline. Runs last.
type Backfiller interface {
	Backfill(ctx context.Context, d *deps.Deps, deadline time.Time) error
}
