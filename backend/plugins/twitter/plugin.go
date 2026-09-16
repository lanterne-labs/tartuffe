package twitter

import (
	"context"
	"errors"
	"fmt"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/plugins"

	"github.com/rs/zerolog/log"
)

// Plugin is the Twitter/X source: handle discovery for tracked persons and
// fact extraction from their tweets.
type Plugin struct{}

// Name implements plugins.Plugin.
func (Plugin) Name() string { return "twitter" }

// NewProviderFromDeps builds a Provider on the shared components. Returns an
// error when the tweet API key is not configured.
func NewProviderFromDeps(d *deps.Deps) (*Provider, error) {
	fetcher, err := NewTwitterAPIIOFetcherFromEnv()
	if err != nil {
		return nil, fmt.Errorf("tweet fetcher: %w", err)
	}
	factExtractor, err := d.FactExtractor()
	if err != nil {
		return nil, fmt.Errorf("fact extractor: %w", err)
	}
	embedder, err := d.Embedder()
	if err != nil {
		return nil, fmt.Errorf("embedder: %w", err)
	}
	return &Provider{Fetcher: fetcher, FactExtractor: factExtractor, Embedder: embedder, Storer: d.Storer, Store: NewStore(d.Storer)}, nil
}

// Ingest implements plugins.Ingester: discovers handles for targets without
// one, then extracts facts from the tweets of every target's handles.
// Discovery is queue-scoped on purpose: an untargeted search over every
// mentioned person produced false links (@ambrusfrance → Alexey Meshkov).
func (Plugin) Ingest(ctx context.Context, d *deps.Deps, targets []plugins.Target, opts plugins.IngestOptions) (plugins.IngestStats, error) {
	var stats plugins.IngestStats
	if len(targets) == 0 {
		log.Info().Msg("twitter: no targets, skipping")
		return stats, nil
	}
	targetIDs := make(map[int64]bool, len(targets))
	ids := make([]int64, 0, len(targets))
	for _, t := range targets {
		targetIDs[t.PersonID] = true
		ids = append(ids, t.PersonID)
	}

	var discoverErr error
	if !opts.DryRun {
		if _, err := Discover(ctx, d, DiscoveryOptions{TargetIDs: ids, Force: opts.Force}); err != nil {
			discoverErr = fmt.Errorf("discovery: %w", err)
			log.Error().Err(discoverErr).Msg("twitter: handle discovery failed")
		}
	}

	handles, err := NewStore(d.Storer).GetActiveHandles(ctx)
	if err != nil {
		return stats, errors.Join(discoverErr, fmt.Errorf("active handles: %w", err))
	}
	provider, err := NewProviderFromDeps(d)
	if err != nil {
		log.Warn().Err(err).Msg("twitter: not configured, skipping collection")
		return stats, discoverErr
	}
	twOpts := Options{
		DateFrom: opts.Since, DateTo: opts.Until, WindowDays: opts.WindowDays,
		DryRun: opts.DryRun, Force: opts.Force, MaxTweets: opts.MaxItems,
	}

	var firstErr error
	seen := make(map[int64]bool)
	for _, h := range handles {
		if !targetIDs[h.PersonID] {
			continue
		}
		var result *Result
		if twOpts.WindowDays > 0 {
			result, err = provider.CollectFactsWithDateWindows(ctx, h, twOpts)
		} else {
			result, err = provider.CollectFacts(ctx, h, twOpts)
		}
		if err != nil {
			log.Error().Err(err).Str("handle", h.Handle).Msg("twitter: collection failed for handle")
			stats.Details = append(stats.Details, "@"+h.Handle+": "+err.Error())
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if !seen[h.PersonID] {
			seen[h.PersonID] = true
			stats.Targets++
		}
		if result != nil {
			stats.Items += result.TweetsFetched
			stats.Facts += result.FactsExtracted
			stats.Errors += len(result.Errors)
		}
	}
	log.Info().Int("handles", len(handles)).Int("persons", stats.Targets).Int("tweets", stats.Items).Int("facts", stats.Facts).
		Msg("twitter: collection completed")
	return stats, errors.Join(discoverErr, firstErr)
}
