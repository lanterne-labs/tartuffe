package search

import (
	"errors"
	"strings"
	"testing"
)

// stubNewsSearcher returns canned results/errors. Tracks call count.
type stubNewsSearcher struct {
	results []NewsResult
	err     error
	calls   int
}

func (s *stubNewsSearcher) SearchNews(query string, opts NewsSearchOptions) ([]NewsResult, error) {
	s.calls++
	return s.results, s.err
}
func (s *stubNewsSearcher) PreviewSearch(query string, opts NewsSearchOptions) (*NewsSearchPreview, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return &NewsSearchPreview{TotalResults: len(s.results), Query: query}, nil
}

func TestChainedNewsSearcher_FirstSucceeds(t *testing.T) {
	a := &stubNewsSearcher{results: []NewsResult{{Title: "A"}}}
	b := &stubNewsSearcher{results: []NewsResult{{Title: "B"}}}

	chain := NewChainedNewsSearcher(
		NamedNewsSearcher{Name: "a", Searcher: a},
		NamedNewsSearcher{Name: "b", Searcher: b},
	)

	results, err := chain.SearchNews("q", NewsSearchOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Title != "A" {
		t.Errorf("expected A's result, got %+v", results)
	}
	if a.calls != 1 {
		t.Errorf("a should have been called once, got %d", a.calls)
	}
	if b.calls != 0 {
		t.Errorf("b should NOT have been called after a succeeded, got %d", b.calls)
	}
}

func TestChainedNewsSearcher_FallthroughOnError(t *testing.T) {
	a := &stubNewsSearcher{err: errors.New("a-failed")}
	b := &stubNewsSearcher{results: []NewsResult{{Title: "B"}}}

	chain := NewChainedNewsSearcher(
		NamedNewsSearcher{Name: "a", Searcher: a},
		NamedNewsSearcher{Name: "b", Searcher: b},
	)

	results, err := chain.SearchNews("q", NewsSearchOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Title != "B" {
		t.Errorf("expected B's result after a failed, got %+v", results)
	}
	if a.calls != 1 || b.calls != 1 {
		t.Errorf("expected both providers called, got a=%d b=%d", a.calls, b.calls)
	}
}

func TestChainedNewsSearcher_AllFail(t *testing.T) {
	a := &stubNewsSearcher{err: errors.New("a-failed")}
	b := &stubNewsSearcher{err: errors.New("b-failed")}

	chain := NewChainedNewsSearcher(
		NamedNewsSearcher{Name: "a", Searcher: a},
		NamedNewsSearcher{Name: "b", Searcher: b},
	)

	_, err := chain.SearchNews("q", NewsSearchOptions{})
	if err == nil {
		t.Fatal("expected error when all providers fail")
	}
	if !strings.Contains(err.Error(), "b-failed") {
		t.Errorf("error should reference last failure: %v", err)
	}
}

func TestChainedNewsSearcher_EmptyChain(t *testing.T) {
	chain := NewChainedNewsSearcher()
	_, err := chain.SearchNews("q", NewsSearchOptions{})
	if err == nil {
		t.Fatal("empty chain should error")
	}
}

func TestChainedNewsSearcher_SkipsNilSearcher(t *testing.T) {
	good := &stubNewsSearcher{results: []NewsResult{{Title: "OK"}}}
	chain := NewChainedNewsSearcher(
		NamedNewsSearcher{Name: "nil", Searcher: nil},
		NamedNewsSearcher{Name: "good", Searcher: good},
	)
	if chain.Len() != 1 {
		t.Errorf("nil searcher should be skipped, got Len=%d", chain.Len())
	}
	results, err := chain.SearchNews("q", NewsSearchOptions{})
	if err != nil || len(results) != 1 {
		t.Errorf("unexpected: results=%v err=%v", results, err)
	}
}

// stubWebSearcher and stubImageSearcher round out the parallel tests.
type stubWebSearcher struct {
	results []SearchResult
	err     error
	calls   int
}

func (s *stubWebSearcher) SearchWeb(query string, limit int) ([]SearchResult, error) {
	s.calls++
	return s.results, s.err
}

func TestChainedWebSearcher_Fallthrough(t *testing.T) {
	a := &stubWebSearcher{err: errors.New("a-failed")}
	b := &stubWebSearcher{results: []SearchResult{{URL: "https://x"}}}
	chain := NewChainedWebSearcher(
		NamedWebSearcher{Name: "a", Searcher: a},
		NamedWebSearcher{Name: "b", Searcher: b},
	)
	results, err := chain.SearchWeb("q", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].URL != "https://x" {
		t.Errorf("expected b's result, got %+v", results)
	}
}

type stubImageSearcher struct {
	results []ImageResult
	err     error
	calls   int
}

func (s *stubImageSearcher) SearchImages(query string, opts ImageSearchOptions) ([]ImageResult, error) {
	s.calls++
	return s.results, s.err
}

func TestChainedImageSearcher_Fallthrough(t *testing.T) {
	a := &stubImageSearcher{err: errors.New("a-failed")}
	b := &stubImageSearcher{results: []ImageResult{{Original: "https://i"}}}
	chain := NewChainedImageSearcher(
		NamedImageSearcher{Name: "a", Searcher: a},
		NamedImageSearcher{Name: "b", Searcher: b},
	)
	results, err := chain.SearchImages("q", ImageSearchOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 || results[0].Original != "https://i" {
		t.Errorf("expected b's result, got %+v", results)
	}
}

func TestChainedNewsSearcher_PreviewSearch(t *testing.T) {
	a := &stubNewsSearcher{err: errors.New("a-down")}
	b := &stubNewsSearcher{results: []NewsResult{{Title: "X"}, {Title: "Y"}}}
	chain := NewChainedNewsSearcher(
		NamedNewsSearcher{Name: "a", Searcher: a},
		NamedNewsSearcher{Name: "b", Searcher: b},
	)
	p, err := chain.PreviewSearch("q", NewsSearchOptions{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.TotalResults != 2 {
		t.Errorf("TotalResults: got %d want 2", p.TotalResults)
	}
}
