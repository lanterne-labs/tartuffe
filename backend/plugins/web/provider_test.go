package web

import (
	"context"
	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/search"
	"testing"
)

type fakeSearch struct{}

func (f *fakeSearch) SearchWithOptions(query string, options map[string]string) ([]search.SearchResult, error) {
	return nil, nil
}

// Lightweight smoke test: ensure CollectFacts handles empty search results gracefully.
func TestCollectFacts_Empty(t *testing.T) {
	p := &Provider{
		Search:  &fakeSearch{},
		FactLLM: &categorizer.OpenAIExtractor{},
	}
	_, _ = p.CollectFacts(context.Background(), "John Doe", map[string]string{"num": "1"})
}
