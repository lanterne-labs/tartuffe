package search

import (
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
)

// SearchResult represents a single item found by a search provider.
// It primarily contains the URL of the discovered content.
type SearchResult struct {
	// URL is the direct link to the found content.
	URL string
	// Title is the title of the search result, if available.
	Title string
	// Snippet is a brief description or snippet from the content, if available.
	Snippet string
}

// SearchProvider defines the interface for searching the web for content related to a person.
// Implementations could use various search engines or APIs.
type SearchProvider interface {
	// Search looks for recent online content mentioning the given personName.
	// It returns a slice of SearchResult structs or an error if the search fails.
	Search(personName string) ([]SearchResult, error)
}

// NewsSearcher is the interface for news search providers (SerpAPI, Brave, etc.).
type NewsSearcher interface {
	SearchNews(query string, opts NewsSearchOptions) ([]NewsResult, error)
	PreviewSearch(query string, opts NewsSearchOptions) (*NewsSearchPreview, error)
}

// WebSearcher is the interface for general web search providers.
type WebSearcher interface {
	SearchWeb(query string, limit int) ([]SearchResult, error)
}

// ImageSearcher is the interface for image search providers.
type ImageSearcher interface {
	SearchImages(query string, opts ImageSearchOptions) ([]ImageResult, error)
}

// NewNewsSearcherFromEnv creates a NewsSearcher based on available environment variables.
// When both BRAVE_SEARCH_API_KEY and SERPAPI_KEY are set, returns a fallback searcher
// that tries Brave first and falls back to SerpAPI on circuit breaker errors.
func NewNewsSearcherFromEnv() (NewsSearcher, error) {
	braveKey := os.Getenv("BRAVE_SEARCH_API_KEY")
	serpKey := os.Getenv("SERPAPI_KEY")

	if braveKey != "" && serpKey != "" {
		log.Info().Msg("Using Brave Search API for news search (SerpAPI fallback)")
		serpapi, err := NewSerpAPINewsProvider(serpKey)
		if err != nil {
			return nil, err
		}
		return &fallbackNewsSearcher{
			primary:  NewBraveSearchProvider(braveKey),
			fallback: serpapi,
		}, nil
	}
	if braveKey != "" {
		log.Info().Msg("Using Brave Search API for news search")
		return NewBraveSearchProvider(braveKey), nil
	}
	if serpKey != "" {
		log.Info().Msg("Using SerpAPI for news search")
		return NewSerpAPINewsProvider(serpKey)
	}
	return nil, fmt.Errorf("no search API key configured: set BRAVE_SEARCH_API_KEY or SERPAPI_KEY")
}

// fallbackNewsSearcher tries the primary NewsSearcher first, falling back to the
// secondary on circuit breaker or quota errors.
type fallbackNewsSearcher struct {
	primary  NewsSearcher
	fallback NewsSearcher
}

func (f *fallbackNewsSearcher) SearchNews(query string, opts NewsSearchOptions) ([]NewsResult, error) {
	results, err := f.primary.SearchNews(query, opts)
	if err != nil && isCircuitBreakerError(err) {
		log.Warn().Str("query", query).Msg("Primary news search failed (circuit breaker), falling back to SerpAPI")
		return f.fallback.SearchNews(query, opts)
	}
	return results, err
}

func (f *fallbackNewsSearcher) PreviewSearch(query string, opts NewsSearchOptions) (*NewsSearchPreview, error) {
	result, err := f.primary.PreviewSearch(query, opts)
	if err != nil && isCircuitBreakerError(err) {
		return f.fallback.PreviewSearch(query, opts)
	}
	return result, err
}

// isCircuitBreakerError checks if an error is from the Brave circuit breaker or a 429.
func isCircuitBreakerError(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "circuit breaker open") || strings.Contains(msg, "429 Too Many Requests")
}

// NewWebSearcherFromEnv creates a WebSearcher based on available environment variables.
// When both keys are set, returns a fallback searcher (Brave primary, SerpAPI fallback).
func NewWebSearcherFromEnv() (WebSearcher, error) {
	braveKey := os.Getenv("BRAVE_SEARCH_API_KEY")
	serpKey := os.Getenv("SERPAPI_KEY")

	if braveKey != "" && serpKey != "" {
		log.Info().Msg("Using Brave Search API for web search (SerpAPI fallback)")
		serpapi, err := NewSerpAPINewsProvider(serpKey)
		if err != nil {
			return nil, err
		}
		return &fallbackWebSearcher{
			primary:  NewBraveSearchProvider(braveKey),
			fallback: serpapi,
		}, nil
	}
	if braveKey != "" {
		log.Info().Msg("Using Brave Search API for web search")
		return NewBraveSearchProvider(braveKey), nil
	}
	if serpKey != "" {
		log.Info().Msg("Using SerpAPI for web search")
		return NewSerpAPINewsProvider(serpKey)
	}
	return nil, fmt.Errorf("no web search API key configured: set BRAVE_SEARCH_API_KEY or SERPAPI_KEY")
}

// fallbackWebSearcher tries Brave first, falls back to SerpAPI on circuit breaker errors.
type fallbackWebSearcher struct {
	primary  WebSearcher
	fallback WebSearcher
}

func (f *fallbackWebSearcher) SearchWeb(query string, limit int) ([]SearchResult, error) {
	results, err := f.primary.SearchWeb(query, limit)
	if err != nil && isCircuitBreakerError(err) {
		log.Warn().Str("query", query).Msg("Primary web search failed (circuit breaker), falling back to SerpAPI")
		return f.fallback.SearchWeb(query, limit)
	}
	return results, err
}

// NewImageSearcherFromEnv creates an ImageSearcher based on available environment variables.
// Priority: BRAVE_SEARCH_API_KEY > SERPAPI_KEY.
func NewImageSearcherFromEnv() (ImageSearcher, error) {
	if key := os.Getenv("BRAVE_SEARCH_API_KEY"); key != "" {
		log.Info().Msg("Using Brave Search API for image search")
		return NewBraveSearchProvider(key), nil
	}
	if key := os.Getenv("SERPAPI_KEY"); key != "" {
		log.Info().Msg("Using SerpAPI for image search")
		return NewSerpAPINewsProvider(key)
	}
	return nil, fmt.Errorf("no search API key configured: set BRAVE_SEARCH_API_KEY or SERPAPI_KEY")
} 