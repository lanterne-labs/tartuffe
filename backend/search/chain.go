package search

import (
	"fmt"

	"github.com/rs/zerolog/log"
)

// chainEntry pairs a provider implementation with a human-readable name for
// logging when it fails and the next provider in the chain is tried.
type chainEntry[T any] struct {
	name     string
	provider T
}

// chainedNewsSearcher tries each NewsSearcher in order until one succeeds.
// If a provider errors, the next is attempted; if all fail, the last error is
// returned wrapped with the chain context. A single-element chain behaves like
// the underlying provider with one extra log line on failure.
type chainedNewsSearcher struct {
	entries []chainEntry[NewsSearcher]
}

// newChainedNewsSearcher builds a chain from (name, provider) pairs. Names are
// only used for logging which provider failed; they have no functional effect.
func newChainedNewsSearcher(pairs ...namedNewsSearcher) *chainedNewsSearcher {
	cs := &chainedNewsSearcher{}
	for _, p := range pairs {
		if p.Searcher == nil {
			continue
		}
		cs.entries = append(cs.entries, chainEntry[NewsSearcher]{name: p.Name, provider: p.Searcher})
	}
	return cs
}

// namedNewsSearcher is a (name, provider) pair for newChainedNewsSearcher.
type namedNewsSearcher struct {
	Name     string
	Searcher NewsSearcher
}

// SearchNews iterates providers; returns the first non-error result.
func (c *chainedNewsSearcher) SearchNews(query string, opts NewsSearchOptions) ([]NewsResult, error) {
	if len(c.entries) == 0 {
		return nil, fmt.Errorf("chained news searcher has no providers")
	}
	var lastErr error
	for _, e := range c.entries {
		results, err := e.provider.SearchNews(query, opts)
		if err == nil {
			return results, nil
		}
		log.Warn().Err(err).Str("provider", e.name).Msg("news search failed, trying next provider in chain")
		lastErr = fmt.Errorf("%s: %w", e.name, err)
	}
	return nil, fmt.Errorf("all news providers failed; last error: %w", lastErr)
}

// chainedWebSearcher tries each WebSearcher in order.
type chainedWebSearcher struct {
	entries []chainEntry[WebSearcher]
}

// namedWebSearcher is a (name, provider) pair for newChainedWebSearcher.
type namedWebSearcher struct {
	Name     string
	Searcher WebSearcher
}

// newChainedWebSearcher builds a chain from (name, provider) pairs.
func newChainedWebSearcher(pairs ...namedWebSearcher) *chainedWebSearcher {
	cs := &chainedWebSearcher{}
	for _, p := range pairs {
		if p.Searcher == nil {
			continue
		}
		cs.entries = append(cs.entries, chainEntry[WebSearcher]{name: p.Name, provider: p.Searcher})
	}
	return cs
}

// SearchWeb iterates providers; returns the first non-error result.
func (c *chainedWebSearcher) SearchWeb(query string, limit int) ([]SearchResult, error) {
	if len(c.entries) == 0 {
		return nil, fmt.Errorf("chained web searcher has no providers")
	}
	var lastErr error
	for _, e := range c.entries {
		results, err := e.provider.SearchWeb(query, limit)
		if err == nil {
			return results, nil
		}
		log.Warn().Err(err).Str("provider", e.name).Msg("web search failed, trying next provider in chain")
		lastErr = fmt.Errorf("%s: %w", e.name, err)
	}
	return nil, fmt.Errorf("all web providers failed; last error: %w", lastErr)
}

// chainedImageSearcher tries each ImageSearcher in order.
type chainedImageSearcher struct {
	entries []chainEntry[ImageSearcher]
}

// namedImageSearcher is a (name, provider) pair for newChainedImageSearcher.
type namedImageSearcher struct {
	Name     string
	Searcher ImageSearcher
}

// newChainedImageSearcher builds a chain from (name, provider) pairs.
func newChainedImageSearcher(pairs ...namedImageSearcher) *chainedImageSearcher {
	cs := &chainedImageSearcher{}
	for _, p := range pairs {
		if p.Searcher == nil {
			continue
		}
		cs.entries = append(cs.entries, chainEntry[ImageSearcher]{name: p.Name, provider: p.Searcher})
	}
	return cs
}

// SearchImages iterates providers; returns the first non-error result.
func (c *chainedImageSearcher) SearchImages(query string, opts ImageSearchOptions) ([]ImageResult, error) {
	if len(c.entries) == 0 {
		return nil, fmt.Errorf("chained image searcher has no providers")
	}
	var lastErr error
	for _, e := range c.entries {
		results, err := e.provider.SearchImages(query, opts)
		if err == nil {
			return results, nil
		}
		log.Warn().Err(err).Str("provider", e.name).Msg("image search failed, trying next provider in chain")
		lastErr = fmt.Errorf("%s: %w", e.name, err)
	}
	return nil, fmt.Errorf("all image providers failed; last error: %w", lastErr)
}
