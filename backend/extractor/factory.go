package extractor

import (
	"fmt"

	"github.com/rs/zerolog/log"
)

// extractorType represents the available content extractor types
type extractorType string

const (
	extractorWebcrawler extractorType = "webcrawler"
	extractorFirecrawl  extractorType = "firecrawl"
	extractorApify      extractorType = "apify"
	// extractorLocal runs in-process: plain HTTP GET + go-trafilatura. No API
	// key, no per-scrape cost, but can't bypass paywalls / anti-bot.
	extractorLocal extractorType = "local"
	// extractorAuto chains localExtractor → WebcrawlerAPI (the latter only if
	// WEBCRAWLERAPI_KEY is set). Use this everywhere unless a specific provider
	// is required for debugging.
	extractorAuto extractorType = "auto"
)

// NewExtractorFromType creates a ContentExtractor based on the specified type.
// Returns an error if the extractor cannot be initialized (e.g., missing API key).
func NewExtractorFromType(kind string) (ContentExtractor, error) {
	switch extractorType(kind) {
	case extractorWebcrawler:
		ext, err := NewWebcrawlerAPIExtractorFromEnv()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize WebcrawlerAPI extractor: %w", err)
		}
		if ext == nil {
			return nil, fmt.Errorf("WEBCRAWLERAPI_KEY not set")
		}
		log.Info().Msg("Using WebcrawlerAPI for content extraction")
		return ext, nil

	case extractorFirecrawl:
		ext, err := NewFirecrawlExtractorFromEnv()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Firecrawl extractor: %w", err)
		}
		if ext == nil {
			return nil, fmt.Errorf("FIRECRAWL_API_KEY not set")
		}
		log.Info().Msg("Using Firecrawl for content extraction")
		return ext, nil

	case extractorApify:
		ext, err := NewApifyExtractorFromEnv()
		if err != nil {
			return nil, fmt.Errorf("failed to initialize Apify extractor: %w", err)
		}
		if ext == nil {
			return nil, fmt.Errorf("APIFY_API_KEY not set")
		}
		log.Info().Msg("Using Apify for content extraction")
		return ext, nil

	case extractorLocal:
		log.Info().Msg("Using LocalExtractor (HTTP + trafilatura) for content extraction")
		return newLocalExtractor(), nil

	case extractorAuto:
		local := newLocalExtractor()
		webcrawl, err := NewWebcrawlerAPIExtractorFromEnv()
		if err != nil {
			return nil, fmt.Errorf("auto extractor: failed to probe WebcrawlerAPI: %w", err)
		}
		// webcrawl may be nil if WEBCRAWLERAPI_KEY is unset — failover will
		// skip nil entries, so we end up with just Local in that case.
		failover, err := newFailoverExtractor(
			failoverEntry{Name: "local", Extractor: local},
			failoverEntry{Name: "webcrawler", Extractor: webcrawl},
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
		return nil, fmt.Errorf("unknown extractor type: %s (valid: auto, local, webcrawler, firecrawl, apify)", kind)
	}
}
