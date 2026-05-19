package entityvalidator

import (
	"context"
	"encoding/json"
	"fmt"

	"digital-mirror/backend/search"
)

// GoogleFilter searches the public web (Serper / Google SERP API) for the
// candidate and attaches the top N result titles + snippets so the LLMFilter
// can weigh them alongside Wikipedia evidence. Like WikiFilter, it ALWAYS
// Defers — the final accept/reject is the LLM's call. Empty results are still
// a valid (and informative) signal.
//
// This filter is omitted from the chain when no SERPER_API_KEY is configured;
// the rest of the pipeline (wiki + LLM) keeps working.
type GoogleFilter struct {
	provider *search.SerperProvider
	maxHits  int
}

// NewGoogleFilter wraps a SerperProvider built from SERPER_API_KEY. Returns
// (nil, error) when the key is missing — call sites can decide whether to
// skip the filter or treat it as fatal.
func NewGoogleFilter() (*GoogleFilter, error) {
	p, err := search.NewSerperProviderFromEnv()
	if err != nil {
		return nil, err
	}
	return &GoogleFilter{provider: p, maxHits: 3}, nil
}

func (g *GoogleFilter) Name() string { return "google" }

// GoogleHit is one organic-search result attached to the LLM's input payload.
type GoogleHit struct {
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
	URL     string `json:"url"`
}

type googleExtra struct {
	Hits []GoogleHit `json:"hits"`
}

func (g *GoogleFilter) Check(_ context.Context, c Candidate) (FilterResult, error) {
	if c.Name == "" {
		return FilterResult{Decision: Defer}, nil
	}

	// One web search; we do NOT recurse with stripped-parens here because
	// Google's keyword match already handles approximate names well.
	results, err := g.provider.SearchWeb(c.Name, g.maxHits)
	if err != nil {
		// Return Defer + the error so the Validator logs it and the LLM gets a
		// "no google evidence" signal rather than a half-empty Extra.
		return FilterResult{Decision: Defer, Reason: "google: " + err.Error()}, err
	}

	hits := make([]GoogleHit, 0, len(results))
	for i, r := range results {
		if i >= g.maxHits {
			break
		}
		hits = append(hits, GoogleHit{
			Title:   r.Title,
			Snippet: r.Snippet,
			URL:     r.URL,
		})
	}
	extra, err := json.Marshal(googleExtra{Hits: hits})
	if err != nil {
		return FilterResult{Decision: Defer, Reason: "google: marshal failed"}, nil
	}
	return FilterResult{
		Decision: Defer,
		Reason:   fmt.Sprintf("google: %d hit(s)", len(hits)),
		Extra:    extra,
	}, nil
}
