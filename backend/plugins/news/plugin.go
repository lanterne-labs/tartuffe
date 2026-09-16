package news

import (
	"context"
	"fmt"

	"digital-mirror/backend/deps"
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
// extracts facts inline. Per-target failures are collected, not returned.
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

	for i, t := range targets {
		log.Info().Int("progress", i+1).Int("total", len(targets)).Str("person", t.Name).Msg("news: target")
		result, err := provider.collect(ctx, t, newsOpts)
		if err != nil {
			log.Error().Err(err).Str("person", t.Name).Msg("news: target failed")
			stats.Details = append(stats.Details, t.Name+": "+err.Error())
			continue
		}
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
