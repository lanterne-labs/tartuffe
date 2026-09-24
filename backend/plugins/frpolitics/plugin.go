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
	// dailyMaxLaws caps the laws summarized per run.
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
// then the transcripts of the last few sitting days.
func (Plugin) Ingest(ctx context.Context, d *deps.Deps, _ []plugins.Target, opts plugins.IngestOptions) (plugins.IngestStats, error) {
	maxLaws := dailyMaxLaws
	if opts.MaxItems > 0 {
		maxLaws = opts.MaxItems
	}
	since := time.Now().AddDate(0, 0, -debatsDailyWindowDays)
	if opts.Since != nil {
		since = *opts.Since
	}
	return ingestVotesThenDebats(
		func() error {
			return Run(ctx, d.Storer, Options{
				Since:          opts.Since,
				MaxLaws:        maxLaws,
				PartyThreshold: dailyPartyThreshold,
			})
		},
		func() (DebatsStats, error) {
			return RunDebats(ctx, d.Storer, DebatsOptions{Since: since, MaxDocs: opts.MaxItems})
		},
	)
}

// ingestVotesThenDebats runs the votes, then the transcripts, and returns
// both failures. Votes that fail on an unreachable LLM (llm.ErrUnavailable)
// skip the transcripts, which need the LLM too.
func ingestVotesThenDebats(votes func() error, debats func() (DebatsStats, error)) (plugins.IngestStats, error) {
	var stats plugins.IngestStats
	votesErr := votes()
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
