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

	default:
		return nil, fmt.Errorf("unknown extractor type: %s (valid: webcrawler, firecrawl, apify)", extractorType)
	}
}
