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

// NewsSearcher is the interface for news search providers (Serper, Brave, SerpAPI, etc.).
type NewsSearcher interface {
	SearchNews(query string, opts NewsSearchOptions) ([]NewsResult, error)
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

// NewNewsSearcherFromEnv assembles a chainedNewsSearcher from whichever API
// keys are present in the environment. Priority: Serper > Brave > SerpAPI.
func NewNewsSearcherFromEnv() (NewsSearcher, error) {
	var pairs []namedNewsSearcher

	if key := os.Getenv(envSerper); key != "" {
		p, err := newSerperProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serper: %w", err)
		}
		pairs = append(pairs, namedNewsSearcher{Name: "serper", Searcher: p})
	}
	if key := os.Getenv(envBrave); key != "" {
		pairs = append(pairs, namedNewsSearcher{Name: "brave", Searcher: newBraveSearchProvider(key)})
	}
	if key := os.Getenv(envSerpAPI); key != "" {
		p, err := newSerpAPINewsProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serpapi: %w", err)
		}
		pairs = append(pairs, namedNewsSearcher{Name: "serpapi", Searcher: p})
	}

	if len(pairs) == 0 {
		return nil, fmt.Errorf("no search API key configured: set %s, %s, or %s", envSerper, envBrave, envSerpAPI)
	}

	logChain("news", pairs)
	return newChainedNewsSearcher(pairs...), nil
}

// NewWebSearcherFromEnv assembles a chainedWebSearcher from available keys.
// Priority: Serper > Brave > SerpAPI.
func NewWebSearcherFromEnv() (WebSearcher, error) {
	var pairs []namedWebSearcher

	if key := os.Getenv(envSerper); key != "" {
		p, err := newSerperProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serper: %w", err)
		}
		pairs = append(pairs, namedWebSearcher{Name: "serper", Searcher: p})
	}
	if key := os.Getenv(envBrave); key != "" {
		pairs = append(pairs, namedWebSearcher{Name: "brave", Searcher: newBraveSearchProvider(key)})
	}
	if key := os.Getenv(envSerpAPI); key != "" {
		p, err := newSerpAPINewsProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serpapi: %w", err)
		}
		pairs = append(pairs, namedWebSearcher{Name: "serpapi", Searcher: p})
	}

	if len(pairs) == 0 {
		return nil, fmt.Errorf("no web search API key configured: set %s, %s, or %s", envSerper, envBrave, envSerpAPI)
	}

	logWebChain(pairs)
	return newChainedWebSearcher(pairs...), nil
}

// NewImageSearcherFromEnv assembles a chainedImageSearcher from available keys.
// Priority: Serper > Brave > SerpAPI.
func NewImageSearcherFromEnv() (ImageSearcher, error) {
	var pairs []namedImageSearcher

	if key := os.Getenv(envSerper); key != "" {
		p, err := newSerperProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serper: %w", err)
		}
		pairs = append(pairs, namedImageSearcher{Name: "serper", Searcher: p})
	}
	if key := os.Getenv(envBrave); key != "" {
		pairs = append(pairs, namedImageSearcher{Name: "brave", Searcher: newBraveSearchProvider(key)})
	}
	if key := os.Getenv(envSerpAPI); key != "" {
		p, err := newSerpAPINewsProvider(key)
		if err != nil {
			return nil, fmt.Errorf("init serpapi: %w", err)
		}
		pairs = append(pairs, namedImageSearcher{Name: "serpapi", Searcher: p})
	}

	if len(pairs) == 0 {
		return nil, fmt.Errorf("no image search API key configured: set %s, %s, or %s", envSerper, envBrave, envSerpAPI)
	}

	logImageChain(pairs)
	return newChainedImageSearcher(pairs...), nil
}

func logChain(kind string, pairs []namedNewsSearcher) {
	names := make([]string, len(pairs))
	for i, p := range pairs {
		names[i] = p.Name
	}
	log.Info().Strs("chain", names).Msgf("Configured %s search chain", kind)
}

func logWebChain(pairs []namedWebSearcher) {
	names := make([]string, len(pairs))
	for i, p := range pairs {
		names[i] = p.Name
	}
	log.Info().Strs("chain", names).Msg("Configured web search chain")
}

func logImageChain(pairs []namedImageSearcher) {
	names := make([]string, len(pairs))
	for i, p := range pairs {
		names[i] = p.Name
	}
	log.Info().Strs("chain", names).Msg("Configured image search chain")
}
