package web

import (
	"context"
	"digital-mirror/backend"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/search"
)

// Provider discovers URLs, extracts content, and yields normalized facts for a person.
type Provider struct {
	Search interface {
		SearchWithOptions(query string, options map[string]string) ([]search.SearchResult, error)
	}
	Extract extractor.ContentExtractor
	FactLLM *categorizer.OpenAIExtractor
}

// FactResult groups an extracted fact with its URL and optional published date.
type FactResult struct {
	Fact backend.Fact
}

// CollectFacts searches for URLs about personName and extracts facts from those pages.
func (p *Provider) CollectFacts(ctx context.Context, personName string, searchOpts map[string]string) ([]FactResult, error) {
	results, err := p.Search.SearchWithOptions(personName+" news", searchOpts)
	if err != nil {
		return nil, err
	}
	out := make([]FactResult, 0)
	for _, r := range results {
		content, err := p.Extract.ExtractContent(r.URL)
		if err != nil || content == nil {
			continue
		}
		facts, err := p.FactLLM.ExtractFacts(ctx, personName, *content)
		if err != nil {
			continue
		}
		for _, f := range facts {
			out = append(out, FactResult{Fact: f})
		}
	}
	return out, nil
}
