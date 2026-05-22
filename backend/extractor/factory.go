package extractor

import (
	"fmt"

	"github.com/rs/zerolog/log"
)

// ExtractorType represents the available content extractor types
type ExtractorType string

const (
	ExtractorWebcrawler ExtractorType = "webcrawler"
	ExtractorFirecrawl  ExtractorType = "firecrawl"
	ExtractorApify      ExtractorType = "apify"
	// ExtractorLocal runs in-process: plain HTTP GET + go-trafilatura. No API
	// key, no per-scrape cost, but can't bypass paywalls / anti-bot.
	ExtractorLocal ExtractorType = "local"
	// ExtractorAuto chains LocalExtractor → WebcrawlerAPI (the latter only if
	// WEBCRAWLERAPI_KEY is set). Use this everywhere unless a specific provider
	// is required for debugging.
	ExtractorAuto ExtractorType = "auto"
)

// NewExtractorFromType creates a ContentExtractor based on the specified type.
// Returns an error if the extractor cannot be initialized (e.g., missing API key).
func NewExtractorFromType(extractorType string) (ContentExtractor, error) {
	switch ExtractorType(extractorType) {
	case ExtractorWebcrawler:
		ext, err := NewWebcrawlerAPIExtractorFromEnv()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize WebcrawlerAPI extractor: %w", err)
		}
		if ext == nil {
			return nil, fmt.Errorf("WEBCRAWLERAPI_KEY not set")
		}
		log.Info().Msg("Using WebcrawlerAPI for content extraction")
		return ext, nil

	case ExtractorFirecrawl:
		ext, err := NewFirecrawlExtractorFromEnv()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Firecrawl extractor: %w", err)
		}
		if ext == nil {
			return nil, fmt.Errorf("FIRECRAWL_API_KEY not set")
		}
		log.Info().Msg("Using Firecrawl for content extraction")
		return ext, nil

	case ExtractorApify:
		ext, err := NewApifyExtractorFromEnv()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Apify extractor: %w", err)
		}
		if ext == nil {
			return nil, fmt.Errorf("APIFY_API_KEY not set")
		}
		log.Info().Msg("Using Apify for content extraction")
		return ext, nil

	case ExtractorLocal:
		log.Info().Msg("Using LocalExtractor (HTTP + trafilatura) for content extraction")
		return NewLocalExtractor(), nil

	case ExtractorAuto:
		local := NewLocalExtractor()
		webcrawl, err := NewWebcrawlerAPIExtractorFromEnv()
		if err != nil {
			return nil, fmt.Errorf("auto extractor: failed to probe WebcrawlerAPI: %w", err)
		}
		// webcrawl may be nil if WEBCRAWLERAPI_KEY is unset — failover will
		// skip nil entries, so we end up with just Local in that case.
		failover, err := NewFailoverExtractor(
			FailoverEntry{Name: "local", Extractor: local},
			FailoverEntry{Name: "webcrawler", Extractor: webcrawl},
		)
		if err != nil {
			return nil, fmt.Errorf("auto extractor: %w", err)
		}
		if webcrawl == nil {
			log.Warn().Msg("Using auto extractor (local only — WEBCRAWLERAPI_KEY not set, no fallback for paywalled/anti-bot sites)")
		} else {
			log.Info().Msg("Using auto extractor (local → webcrawler fallback)")
		}
		return failover, nil

	default:
		return nil, fmt.Errorf("unknown extractor type: %s (valid: auto, local, webcrawler, firecrawl, apify)", extractorType)
	}
}
