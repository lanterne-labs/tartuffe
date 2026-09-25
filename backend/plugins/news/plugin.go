package news

import (
	"context"
	"errors"
	"fmt"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins"

	"github.com/rs/zerolog/log"
)

// Plugin is the news source: web news search per tracked target, article
// scraping and LLM fact extraction.
type Plugin struct{}

// Name implements plugins.Plugin.
func (Plugin) Name() string { return "news" }

const (
	// dailyMaxParse is how many articles a run parses per target.
	dailyMaxParse = 5
	// defaultLocation restricts search results to the project's country.
	defaultLocation = "France"
	// neverRevisitHours makes an already-ingested article URL final.
	neverRevisitHours = 24 * 365 * 10
)

// NewProviderFromDeps builds a Provider on the shared components.
func NewProviderFromDeps(d *deps.Deps) (*Provider, error) {
	searcher, err := d.NewsSearcher()
	if err != nil {
		return nil, fmt.Errorf("news search: %w", err)
	}
	contentExtractor, err := d.ContentExtractor()
	if err != nil {
		return nil, fmt.Errorf("content extractor: %w", err)
	}
	factExtractor, err := d.FactExtractor()
	if err != nil {
		return nil, fmt.Errorf("fact extractor: %w", err)
	}
	embedder, err := d.Embedder()
	if err != nil {
		return nil, fmt.Errorf("embedder: %w", err)
	}
	stanceMgr, err := d.StanceManager()
	if err != nil {
		return nil, fmt.Errorf("stance manager: %w", err)
	}
	return NewProvider(searcher, contentExtractor, factExtractor, embedder, d.Storer, stanceMgr), nil
}

// Ingest implements plugins.Ingester: searches news for every target and
// extracts facts inline. Per-target failures are collected, not returned,
// except an unreachable LLM, which stops the run (see ingestTargets).
func (Plugin) Ingest(ctx context.Context, d *deps.Deps, targets []plugins.Target, opts plugins.IngestOptions) (plugins.IngestStats, error) {
	var stats plugins.IngestStats
	if len(targets) == 0 {
		log.Info().Msg("news: no targets, skipping")
		return stats, nil
	}
	provider, err := NewProviderFromDeps(d)
	if err != nil {
		return stats, err
	}
	newsOpts := Options{
		DateFrom:     opts.Since,
		DateTo:       opts.Until,
		WindowDays:   opts.WindowDays,
		DryRun:       opts.DryRun,
		MaxParse:     dailyMaxParse,
		Location:     defaultLocation,
		RevisitHours: neverRevisitHours,
	}
	if opts.Force {
		newsOpts.RevisitHours = 0
	}
	if opts.MaxItems > 0 {
		newsOpts.MaxParse = opts.MaxItems
	}

	return ingestTargets(ctx, targets, provider.resumeCollect(opts.ExtractOnly, newsOpts))
}

// resumeCollect is the per-target collection of Ingest: a target in
// extractOnly (its search and scrape completed before an LLM outage) only has
// its stored documents extracted, without a new search or scrape.
func (p *Provider) resumeCollect(extractOnly []int64, opts Options) func(context.Context, plugins.Target) (*Result, error) {
	fetched := make(map[int64]bool, len(extractOnly))
	for _, id := range extractOnly {
		fetched[id] = true
	}
	return func(ctx context.Context, t plugins.Target) (*Result, error) {
		if fetched[t.PersonID] {
			log.Info().Str("person", t.Name).Msg("news: resuming the extraction of the stored documents")
			result := &Result{Target: t}
			return result, p.extractStoredDocuments(ctx, t, p.currentParties(ctx, t), opts, result)
		}
		return p.collect(ctx, t, opts)
	}
}

// ingestTargets runs collect on every target. Per-target failures are
// collected in the stats; an unreachable LLM (llm.ErrUnavailable) stops the
// loop and is returned. stats.Done lists every target finished before the
// stop. The LLM is only called after a target's search and scrape (its
// articles are stored as documents first), so the interrupted target goes to
// stats.Fetched: a retry only extracts its stored documents. (A date-windowed
// collection leaves its later windows to the next run.)
func ingestTargets(ctx context.Context, targets []plugins.Target, collect func(context.Context, plugins.Target) (*Result, error)) (plugins.IngestStats, error) {
	var stats plugins.IngestStats
	for i, t := range targets {
		log.Info().Int("progress", i+1).Int("total", len(targets)).Str("person", t.Name).Msg("news: target")
		result, err := collect(ctx, t)
		if err != nil {
			log.Error().Err(err).Str("person", t.Name).Msg("news: target failed")
			if errors.Is(err, llm.ErrUnavailable) {
				stats.Fetched = append(stats.Fetched, t.PersonID)
				return stats, fmt.Errorf("%s: %w", t.Name, err)
			}
			stats.Details = append(stats.Details, t.Name+": "+err.Error())
			stats.Done = append(stats.Done, t.PersonID)
			continue
		}
		stats.Done = append(stats.Done, t.PersonID)
		stats.Targets++
		if result == nil {
			continue
		}
		stats.Items += result.ArticlesParsed
		stats.Errors += len(result.Errors)
		stats.Facts += result.FactsExtracted
		log.Info().Str("person", t.Name).
			Int("articles_found", result.ArticlesFound).Int("articles_parsed", result.ArticlesParsed).
			Int("facts_extracted", result.FactsExtracted).Msg("news: target done")
	}
	return stats, nil
}
