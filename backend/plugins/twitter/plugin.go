package twitter

import (
	"context"
	"errors"
	"fmt"
	"time"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins"
	"digital-mirror/backend/search"

	"github.com/rs/zerolog/log"
)

// Plugin is the Twitter/X source: handle discovery for tracked persons and
// fact extraction from their tweets.
type Plugin struct{}

// Name implements plugins.Plugin.
func (Plugin) Name() string { return "twitter" }

// HistoryFetched implements plugins.HistoryReader: every day of the stretch
// is in the fetch log of every active handle of the targets.
func (Plugin) HistoryFetched(ctx context.Context, d *deps.Deps, targets []plugins.Target, since, until time.Time) (bool, error) {
	return NewStore(d.Storer).HandlesFetchedCover(ctx, plugins.TargetIDs(targets), since, until)
}

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
	ids := make([]int64, 0, len(targets))
	for _, t := range targets {
		ids = append(ids, t.PersonID)
	}

	var discoverErr error
	if !opts.DryRun {
		if _, err := Discover(ctx, d, DiscoveryOptions{TargetIDs: ids, Force: opts.Force}); err != nil {
			discoverErr = fmt.Errorf("discovery: %w", err)
			log.Error().Err(discoverErr).Msg("twitter: handle discovery failed")
			if errors.Is(err, llm.ErrUnavailable) {
				return stats, discoverErr
			}
			if ctx.Err() != nil {
				return stats, fmt.Errorf("twitter stopped in discovery: %w", ctx.Err())
			}
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

	stats, err = collectHandles(ctx, handles, ids, func(ctx context.Context, h Handle) (*Result, error) {
		if twOpts.WindowDays > 0 {
			return provider.CollectFactsWithDateWindows(ctx, h, twOpts)
		}
		return provider.CollectFacts(ctx, h, twOpts)
	})
	// A target without a handle has nothing to fetch: the run says so, so a
	// collection task done with nothing fetched is not taken for a success.
	stats.Details = append(stats.Details, withoutHandle(targets, handles)...)
	log.Info().Int("handles", len(handles)).Int("persons", stats.Targets).Int("tweets", stats.Items).Int("facts", stats.Facts).
		Msg("twitter: collection completed")
	return stats, errors.Join(discoverErr, err)
}

// withoutHandle lists, one line per target, the targets with no active handle.
func withoutHandle(targets []plugins.Target, handles []Handle) []string {
	has := make(map[int64]bool, len(handles))
	for _, h := range handles {
		has[h.PersonID] = true
	}
	var out []string
	for _, t := range targets {
		if !has[t.PersonID] {
			out = append(out, t.Name+": no twitter handle")
		}
	}
	return out
}

// collectHandles runs collect on the handles of the target persons. A failed
// handle is recorded and the loop goes on, except on an unreachable LLM
// (llm.ErrUnavailable): every further handle would fetch tweets (API credits)
// only to leave them unextracted, so the loop stops and returns it with the
// first failure; so do the API's credits run out (search.ErrQuota: no later
// handle is served, and the worker lets the task wait for them) and a
// cancelled ctx, the cut handle not counted as failed. Otherwise the first failure is returned. stats.Done lists the
// targets whose every handle was collected or failed before the stop.
func collectHandles(ctx context.Context, handles []Handle, targetIDs []int64, collect func(context.Context, Handle) (*Result, error)) (plugins.IngestStats, error) {
	var stats plugins.IngestStats
	var firstErr error
	// left counts each target's handles not finished yet.
	left := make(map[int64]int, len(targetIDs))
	for _, id := range targetIDs {
		left[id] = 0
	}
	for _, h := range handles {
		if _, ok := left[h.PersonID]; ok {
			left[h.PersonID]++
		}
	}
	done := func() []int64 {
		var out []int64
		for _, id := range targetIDs {
			if left[id] == 0 {
				out = append(out, id)
			}
		}
		return out
	}
	seen := make(map[int64]bool)
	for _, h := range handles {
		if _, ok := left[h.PersonID]; !ok {
			continue
		}
		if err := ctx.Err(); err != nil {
			stats.Done = done()
			return stats, errors.Join(firstErr, fmt.Errorf("twitter stopped before @%s: %w", h.Handle, err))
		}
		result, err := collect(ctx, h)
		if err != nil && ctx.Err() != nil {
			stats.Done = done()
			return stats, errors.Join(firstErr, fmt.Errorf("twitter stopped at @%s: %w", h.Handle, ctx.Err()))
		}
		if err != nil {
			log.Error().Err(err).Str("handle", h.Handle).Msg("twitter: collection failed for handle")
			stats.Details = append(stats.Details, "@"+h.Handle+": "+err.Error())
			if errors.Is(err, llm.ErrUnavailable) || errors.Is(err, search.ErrQuota) {
				// The LLM is down or the API's credits ran out: no later
				// handle is served either.
				stats.Done = done()
				return stats, errors.Join(firstErr, err)
			}
			if firstErr == nil {
				firstErr = err
			}
			left[h.PersonID]--
			continue
		}
		left[h.PersonID]--
		if !seen[h.PersonID] {
			seen[h.PersonID] = true
			stats.Targets++
		}
		if result != nil {
			stats.Items += result.TweetsFetched
			stats.Facts += result.FactsExtracted
			stats.Errors += len(result.Errors)
			if len(result.Errors) > 0 {
				// The first error names the cause in the task's last_error:
				// on 2026-11-03 the API's credits ran out (402) and the
				// failed tasks said only "1 item or window errors".
				stats.Details = append(stats.Details, "@"+h.Handle+": "+result.Errors[0].Error())
			}
		}
	}
	stats.Done = done()
	return stats, firstErr
}
