package worker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/pipeline"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/storage"
)

// WindowDays splits the period of a task into the windows each search
// covers, as the history step of the nightly did.
const WindowDays = 3

// PluginRunner runs a task through the ingest of its plugin, with the
// period of the task, and reads the plugin's fetch log for the coverage.
type PluginRunner struct {
	Deps *deps.Deps
}

// PeriodPlugins lists the registered plugins that read their source by date
// range: the plugins a task can name.
func PeriodPlugins() []string {
	var out []string
	for _, p := range pipeline.Plugins {
		if _, ok := p.(plugins.HistoryReader); ok {
			out = append(out, p.Name())
		}
	}
	return out
}

// Run implements Runner.
func (r PluginRunner) Run(ctx context.Context, task storage.CollectionTask) (Outcome, error) {
	p, err := pipeline.Find(task.Plugin)
	if err != nil {
		return Outcome{}, err
	}
	hr, ok := p.(plugins.HistoryReader)
	if !ok {
		return Outcome{}, fmt.Errorf("plugin %q does not read its source by period", task.Plugin)
	}
	targets := []plugins.Target{{PersonID: task.PersonID, Name: task.PersonName, EntityType: task.EntityType}}
	since, until := task.Since, task.Until
	opts := plugins.IngestOptions{Since: &since, Until: &until, WindowDays: WindowDays}

	var in pipeline.IngestRun
	err = in.Run(ctx, r.Deps, targets, opts, []string{task.Plugin})
	if err == nil {
		// A failure of the plugin itself is recorded, not returned, by Run.
		err = in.Errs[task.Plugin]
	}
	stats := in.Result.Total
	out := Outcome{Items: stats.Items, Facts: stats.Facts, Details: stats.Details}
	if stats.Errors > 0 {
		// The per-window failures (a search quota) are counted, not listed.
		out.Details = append(out.Details, fmt.Sprintf("%d item or window errors", stats.Errors))
	}
	if ctx.Err() != nil || errors.Is(err, llm.ErrUnavailable) {
		return out, err
	}
	// Read even after a failure: the windows fetched before it are in the log.
	covered, cErr := hr.HistoryFetched(context.WithoutCancel(ctx), r.Deps, targets, since, until)
	if cErr != nil {
		return out, errors.Join(err, fmt.Errorf("reading the fetch log: %w", cErr))
	}
	out.Covered = covered
	return out, err
}

// Day returns t as a whole UTC day.
func Day(t time.Time) time.Time {
	return t.UTC().Truncate(24 * time.Hour)
}
