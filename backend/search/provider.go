package search

import (
	"fmt"
	"os"

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

// ImageSearcher is the interface for image search providers.
type ImageSearcher interface {
	SearchImages(query string, opts ImageSearchOptions) ([]ImageResult, error)
}

// NewNewsSearcherFromEnv creates a NewsSearcher based on available environment variables.
// Priority: BRAVE_SEARCH_API_KEY > SERPAPI_KEY.
func NewNewsSearcherFromEnv() (NewsSearcher, error) {
	if key := os.Getenv("BRAVE_SEARCH_API_KEY"); key != "" {
		log.Info().Msg("Using Brave Search API for news search")
		return NewBraveSearchProvider(key), nil
	}
	if key := os.Getenv("SERPAPI_KEY"); key != "" {
		log.Info().Msg("Using SerpAPI for news search")
		return NewSerpAPINewsProvider(key)
	}
	return nil, fmt.Errorf("no search API key configured: set BRAVE_SEARCH_API_KEY or SERPAPI_KEY")
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