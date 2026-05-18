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

// NewsSearcher is the interface for news search providers (Serper, Brave, SerpAPI, etc.).
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

// Priority for chain assembly: providers earlier in the list are tried first.
// Adding a new provider = appending one more case here + setting its env var.
const (
	envSerper  = "SERPER_API_KEY"
	envBrave   = "BRAVE_SEARCH_API_KEY"
	envSerpAPI = "SERPAPI_KEY"
)

// NewNewsSearcherFromEnv assembles a ChainedNewsSearcher from whichever API
// keys are present in the environment. Priority: Serper > Brave > SerpAPI.
func NewNewsSearcherFromEnv() (NewsSearcher, error) {
	var pairs []NamedNewsSearcher

	if key := os.Getenv(envSerper); key != "" {
		p, err := NewSerperProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serper: %w", err)
		}
		pairs = append(pairs, NamedNewsSearcher{Name: "serper", Searcher: p})
	}
	if key := os.Getenv(envBrave); key != "" {
		pairs = append(pairs, NamedNewsSearcher{Name: "brave", Searcher: NewBraveSearchProvider(key)})
	}
	if key := os.Getenv(envSerpAPI); key != "" {
		p, err := NewSerpAPINewsProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serpapi: %w", err)
		}
		pairs = append(pairs, NamedNewsSearcher{Name: "serpapi", Searcher: p})
	}

	if len(pairs) == 0 {
		return nil, fmt.Errorf("no search API key configured: set %s, %s, or %s", envSerper, envBrave, envSerpAPI)
	}

	logChain("news", pairs)
	return NewChainedNewsSearcher(pairs...), nil
}

// NewWebSearcherFromEnv assembles a ChainedWebSearcher from available keys.
// Priority: Serper > Brave > SerpAPI.
func NewWebSearcherFromEnv() (WebSearcher, error) {
	var pairs []NamedWebSearcher

	if key := os.Getenv(envSerper); key != "" {
		p, err := NewSerperProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serper: %w", err)
		}
		pairs = append(pairs, NamedWebSearcher{Name: "serper", Searcher: p})
	}
	if key := os.Getenv(envBrave); key != "" {
		pairs = append(pairs, NamedWebSearcher{Name: "brave", Searcher: NewBraveSearchProvider(key)})
	}
	if key := os.Getenv(envSerpAPI); key != "" {
		p, err := NewSerpAPINewsProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serpapi: %w", err)
		}
		pairs = append(pairs, NamedWebSearcher{Name: "serpapi", Searcher: p})
	}

	if len(pairs) == 0 {
		return nil, fmt.Errorf("no web search API key configured: set %s, %s, or %s", envSerper, envBrave, envSerpAPI)
	}

	logWebChain(pairs)
	return NewChainedWebSearcher(pairs...), nil
}

// NewImageSearcherFromEnv assembles a ChainedImageSearcher from available keys.
// Priority: Serper > Brave > SerpAPI.
func NewImageSearcherFromEnv() (ImageSearcher, error) {
	var pairs []NamedImageSearcher

	if key := os.Getenv(envSerper); key != "" {
		p, err := NewSerperProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serper: %w", err)
		}
		pairs = append(pairs, NamedImageSearcher{Name: "serper", Searcher: p})
	}
	if key := os.Getenv(envBrave); key != "" {
		pairs = append(pairs, NamedImageSearcher{Name: "brave", Searcher: NewBraveSearchProvider(key)})
	}
	if key := os.Getenv(envSerpAPI); key != "" {
		p, err := NewSerpAPINewsProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serpapi: %w", err)
		}
		pairs = append(pairs, NamedImageSearcher{Name: "serpapi", Searcher: p})
	}

	if len(pairs) == 0 {
		return nil, fmt.Errorf("no image search API key configured: set %s, %s, or %s", envSerper, envBrave, envSerpAPI)
	}

	logImageChain(pairs)
	return NewChainedImageSearcher(pairs...), nil
}

func logChain(kind string, pairs []NamedNewsSearcher) {
	names := make([]string, len(pairs))
	for i, p := range pairs {
		names[i] = p.Name
	}
	log.Info().Strs("chain", names).Msgf("Configured %s search chain", kind)
}

func logWebChain(pairs []NamedWebSearcher) {
	names := make([]string, len(pairs))
	for i, p := range pairs {
		names[i] = p.Name
	}
	log.Info().Strs("chain", names).Msg("Configured web search chain")
}

func logImageChain(pairs []NamedImageSearcher) {
	names := make([]string, len(pairs))
	for i, p := range pairs {
		names[i] = p.Name
	}
	log.Info().Strs("chain", names).Msg("Configured image search chain")
}
