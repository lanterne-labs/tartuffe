package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/plugins"

	"github.com/rs/zerolog/log"
)

const (
	// historyDepthYears is how far back the history step reads.
	historyDepthYears = 3
	// historyStepDays is the stretch of history read per run and plugin.
	historyStepDays = 30
	// historyWindowDays splits that stretch into the windows each search
	// covers, the same as the daily collection's.
	historyWindowDays = 3
)

// HistoryCursors persists how far back each plugin's history has been read
// (*storage.PostgresStorer implements it).
type HistoryCursors interface {
	GetHistoryCursor(ctx context.Context, plugin string) (reached time.Time, ok bool, err error)
	SetHistoryCursor(ctx context.Context, plugin string, reached time.Time) error
}

// historyWindow is the next stretch of history to read before a plugin's
// cursor: historyStepDays ending at reached (at now when its history has
// never been read), floored at historyDepthYears before now. ok is false once
// the cursor reached the floor.
func historyWindow(reached time.Time, read bool, now time.Time) (since, until time.Time, ok bool) {
	floor := now.AddDate(-historyDepthYears, 0, 0)
	until = now
	if read {
		until = reached
	}
	if !until.After(floor) {
		return time.Time{}, time.Time{}, false
	}
	since = until.AddDate(0, 0, -historyStepDays)
	if since.Before(floor) {
		since = floor
	}
	return since, until, true
}

// runHistory reads, for every plugin that can read its source by date range
// (plugins.HistoryReader), the stretch of history before its cursor for
// targets, through the same resumable ingest as the daily collection; run
// passes each read through the run gate (deadline, LLM-outage wait). The
// plugin's own fetch log decides when a stretch is done (HistoryFetched):
// the cursor moves back over every stretch fetched for every target, so a
// read cut by the deadline or an outage resumes where it stopped, and a
// stretch with a target the plugin could not fetch (an error, a search quota)
// is read again, never skipped.
func runHistory(ctx context.Context, d *deps.Deps, cursors HistoryCursors, targets []plugins.Target, now time.Time, run func(phase string, fn func() error) error) StepErrors {
	errs := StepErrors{}
	if len(targets) == 0 {
		log.Warn().Msg("History: no targets; nothing read")
		return errs
	}
	// Whole days, so that the plugins' per-day fetch logs match the stretches.
	now = now.UTC().Truncate(24 * time.Hour)
	for _, p := range Plugins {
		hr, ok := p.(plugins.HistoryReader)
		if !ok {
			continue
		}
		name := p.Name()
		step := "history-" + name
		if err := readHistory(ctx, d, cursors, hr, name, step, targets, now, run); err != nil {
			errs[step] = err
		}
	}
	return errs
}

// readHistory walks name's cursor back: over each stretch already fetched
// for every target, and over each stretch it reads and then finds fetched.
// It goes on, a stretch at a time, until a stretch stays unfetched (an error,
// a cut, a closed gate) or the full depth is reached. It returns the read
// errors, fetched stretches included (their extraction failed or was cut:
// the stored items are extracted from the backlog).
func readHistory(ctx context.Context, d *deps.Deps, cursors HistoryCursors, hr plugins.HistoryReader, name, step string,
	targets []plugins.Target, now time.Time, run func(phase string, fn func() error) error) error {
	reached, read, err := cursors.GetHistoryCursor(ctx, name)
	if err != nil {
		return err
	}
	var readErrs error
	for {
		since, until, ok := historyWindow(reached, read, now)
		if !ok {
			log.Info().Str("plugin", name).Time("reached", reached).Msg("History: read back to the full depth; nothing left")
			return readErrs
		}
		fetched, err := hr.HistoryFetched(ctx, d, targets, since, until)
		if err != nil {
			return errors.Join(readErrs, err)
		}
		if !fetched {
			log.Info().Str("plugin", name).Time("since", since).Time("until", until).Int("targets", len(targets)).
				Msg("History: reading the stretch before the cursor")
			opts := plugins.IngestOptions{Since: &since, Until: &until, WindowDays: historyWindowDays}
			var in IngestRun
			readErr := run(step, func() error { return in.Run(ctx, d, targets, opts, []string{name}) })
			// The plugin's own error carries the outage a failed read returns.
			if pErr := in.Errs[name]; pErr != nil {
				readErr = pErr
			}
			readErrs = errors.Join(readErrs, readErr)
			// Checked even after a cut: what the read fetched is in the log.
			if fetched, err = hr.HistoryFetched(context.WithoutCancel(ctx), d, targets, since, until); err != nil {
				return errors.Join(readErrs, err)
			}
			if !fetched {
				log.Warn().Err(readErr).Str("plugin", name).Time("since", since).
					Msg("History: stretch not fully fetched; the next run goes on with it")
				return readErrs
			}
		}
		if err := cursors.SetHistoryCursor(context.WithoutCancel(ctx), name, since); err != nil {
			return errors.Join(readErrs, err)
		}
		log.Info().Str("plugin", name).Time("reached", since).Msg("History: cursor moved back")
		reached, read = since, true
		if ctx.Err() != nil {
			return errors.Join(readErrs, fmt.Errorf("history %s stopped: %w", name, ctx.Err()))
		}
	}
}
