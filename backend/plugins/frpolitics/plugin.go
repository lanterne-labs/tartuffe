package frpolitics

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

// Plugin is the Assemblée nationale source: scrutins (votes) and séance
// transcripts (débats). It has its own scope, so the run's targets are
// ignored.
type Plugin struct{}

// Name implements plugins.Plugin.
func (Plugin) Name() string { return "frpolitics" }

const (
	// dailyMaxLaws caps the laws summarized per run. Every ballot is
	// stance-routed at insert, ~65 min of GPU per law (run 110: 14 laws in
	// 15 h); 30 kept the run in ingest for 28 h (run 111).
	dailyMaxLaws = 10
	// dailyPartyThreshold is the majority share for party-level vote facts.
	dailyPartyThreshold = 0.8
	// debatsDailyWindowDays is how far back the daily transcript feed looks.
	// The AN archive is refreshed around 02:05 UTC and the run starts at
	// 01:00 UTC, so "yesterday" is often only available the following night.
	debatsDailyWindowDays = 4
	// debatsBackfillSince is the start of the session the backfill covers.
	debatsBackfillSince = "2025-10-01"
)

// Ingest implements plugins.Ingester: the votes of newly published scrutins,
// then the transcripts of the last few sitting days. On a retry after an LLM
// outage the laws earlier calls went through (opts.Spent) come off the
// per-run law cap, and the votes are skipped once it is used up.
func (Plugin) Ingest(ctx context.Context, d *deps.Deps, _ []plugins.Target, opts plugins.IngestOptions) (plugins.IngestStats, error) {
	maxLaws := lawsLeft(opts)
	since := time.Now().AddDate(0, 0, -debatsDailyWindowDays)
	if opts.Since != nil {
		since = *opts.Since
	}
	return ingestVotesThenDebats(
		func() (int, error) {
			if maxLaws <= 0 {
				log.Info().Int("laws_done", opts.Spent).Msg("frpolitics: the run's law cap is used up; votes skipped")
				return 0, nil
			}
			return Run(ctx, d.Storer, Options{
				Since:          opts.Since,
				MaxLaws:        maxLaws,
				PartyThreshold: dailyPartyThreshold,
				// Always visitable: a law whose store stopped part-way (an LLM
				// outage) resumes on the pipeline's retry of the same run, after
				// its stored ballots, instead of waiting out the 24h revisit
				// window behind its own fact_sources rows. Settled laws are
				// excluded by processed_laws, not by this window.
				RevisitHours: -1,
			})
		},
		func() (DebatsStats, error) {
			return RunDebats(ctx, d.Storer, DebatsOptions{Since: since, MaxDocs: opts.MaxItems})
		},
	)
}

// lawsLeft is the run's law cap (dailyMaxLaws, or MaxItems) minus the laws
// earlier calls of the run went through.
func lawsLeft(opts plugins.IngestOptions) int {
	maxLaws := dailyMaxLaws
	if opts.MaxItems > 0 {
		maxLaws = opts.MaxItems
	}
	return maxLaws - opts.Spent
}

// ingestVotesThenDebats runs the votes, then the transcripts, and returns
// both failures; stats.Spent is the laws the votes went through. Votes that
// fail on an unreachable LLM (llm.ErrUnavailable) skip the transcripts, which
// need the LLM too.
func ingestVotesThenDebats(votes func() (int, error), debats func() (DebatsStats, error)) (plugins.IngestStats, error) {
	var stats plugins.IngestStats
	laws, votesErr := votes()
	stats.Spent = laws
	if votesErr != nil {
		votesErr = fmt.Errorf("votes: %w", votesErr)
		log.Error().Err(votesErr).Msg("frpolitics: votes failed")
		if errors.Is(votesErr, llm.ErrUnavailable) {
			return stats, votesErr
		}
	}

	ds, debatsErr := debats()
	stats.Items += ds.Processed
	stats.Facts += ds.Facts
	stats.Errors += ds.Errors
	if debatsErr != nil {
		debatsErr = fmt.Errorf("debats: %w", debatsErr)
		log.Error().Err(debatsErr).Msg("frpolitics: débats daily feed failed")
	}
	return stats, errors.Join(votesErr, debatsErr)
}

// ProducePairs implements plugins.PairProducer: every (what they said in a
// séance, how they voted in that séance) pair per person.
func (Plugin) ProducePairs(ctx context.Context, d *deps.Deps) (int, error) {
	return EnqueueSameSeancePairs(ctx, d.Storer)
}

// Backfill implements plugins.Backfiller: transcripts of the current session,
// speakers with press facts first, newest séances first, until the deadline.
func (Plugin) Backfill(ctx context.Context, d *deps.Deps, deadline time.Time) error {
	since, err := time.Parse("2006-01-02", debatsBackfillSince)
	if err != nil {
		return fmt.Errorf("debatsBackfillSince: %w", err)
	}
	stats, err := RunDebats(ctx, d.Storer, DebatsOptions{
		Since: since, NewestFirst: true, Deadline: deadline, PrioritizePress: true,
	})
	log.Info().Int("pending_before", stats.Pending).Int("processed", stats.Processed).Int("facts", stats.Facts).
		Int("errors", stats.Errors).Int("pending_after", stats.PendingAfter).Bool("deadline_hit", stats.DeadlineHit).
		Msg("frpolitics: débats backfill done")
	return err
}
