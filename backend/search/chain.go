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

// ChainedNewsSearcher tries each NewsSearcher in order until one succeeds.
// If a provider errors, the next is attempted; if all fail, the last error is
// returned wrapped with the chain context. A single-element chain behaves like
// the underlying provider with one extra log line on failure.
type ChainedNewsSearcher struct {
	entries []chainEntry[NewsSearcher]
}

// NewChainedNewsSearcher builds a chain from (name, provider) pairs. Names are
// only used for logging which provider failed; they have no functional effect.
func NewChainedNewsSearcher(pairs ...NamedNewsSearcher) *ChainedNewsSearcher {
	cs := &ChainedNewsSearcher{}
	for _, p := range pairs {
		if p.Searcher == nil {
			continue
		}
		cs.entries = append(cs.entries, chainEntry[NewsSearcher]{name: p.Name, provider: p.Searcher})
	}
	return cs
}

// NamedNewsSearcher is a (name, provider) pair for NewChainedNewsSearcher.
type NamedNewsSearcher struct {
	Name     string
	Searcher NewsSearcher
}

// Len returns the number of providers in the chain.
func (c *ChainedNewsSearcher) Len() int { return len(c.entries) }

// SearchNews iterates providers; returns the first non-error result.
func (c *ChainedNewsSearcher) SearchNews(query string, opts NewsSearchOptions) ([]NewsResult, error) {
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

// PreviewSearch iterates providers; returns the first non-error preview.
func (c *ChainedNewsSearcher) PreviewSearch(query string, opts NewsSearchOptions) (*NewsSearchPreview, error) {
	if len(c.entries) == 0 {
		return nil, fmt.Errorf("chained news searcher has no providers")
	}
	var lastErr error
	for _, e := range c.entries {
		result, err := e.provider.PreviewSearch(query, opts)
		if err == nil {
			return result, nil
		}
		log.Warn().Err(err).Str("provider", e.name).Msg("news preview failed, trying next provider in chain")
		lastErr = fmt.Errorf("%s: %w", e.name, err)
	}
	return nil, fmt.Errorf("all news providers failed; last error: %w", lastErr)
}

// ChainedWebSearcher tries each WebSearcher in order.
type ChainedWebSearcher struct {
	entries []chainEntry[WebSearcher]
}

// NamedWebSearcher is a (name, provider) pair for NewChainedWebSearcher.
type NamedWebSearcher struct {
	Name     string
	Searcher WebSearcher
}

// NewChainedWebSearcher builds a chain from (name, provider) pairs.
func NewChainedWebSearcher(pairs ...NamedWebSearcher) *ChainedWebSearcher {
	cs := &ChainedWebSearcher{}
	for _, p := range pairs {
		if p.Searcher == nil {
			continue
		}
		cs.entries = append(cs.entries, chainEntry[WebSearcher]{name: p.Name, provider: p.Searcher})
	}
	return cs
}

// Len returns the number of providers in the chain.
func (c *ChainedWebSearcher) Len() int { return len(c.entries) }

// SearchWeb iterates providers; returns the first non-error result.
func (c *ChainedWebSearcher) SearchWeb(query string, limit int) ([]SearchResult, error) {
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

// ChainedImageSearcher tries each ImageSearcher in order.
type ChainedImageSearcher struct {
	entries []chainEntry[ImageSearcher]
}

// NamedImageSearcher is a (name, provider) pair for NewChainedImageSearcher.
type NamedImageSearcher struct {
	Name     string
	Searcher ImageSearcher
}

// NewChainedImageSearcher builds a chain from (name, provider) pairs.
func NewChainedImageSearcher(pairs ...NamedImageSearcher) *ChainedImageSearcher {
	cs := &ChainedImageSearcher{}
	for _, p := range pairs {
		if p.Searcher == nil {
			continue
		}
		cs.entries = append(cs.entries, chainEntry[ImageSearcher]{name: p.Name, provider: p.Searcher})
	}
	return cs
}

// Len returns the number of providers in the chain.
func (c *ChainedImageSearcher) Len() int { return len(c.entries) }

// SearchImages iterates providers; returns the first non-error result.
func (c *ChainedImageSearcher) SearchImages(query string, opts ImageSearchOptions) ([]ImageResult, error) {
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
