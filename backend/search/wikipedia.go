package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

// WikipediaSummary holds key information extracted from a Wikipedia page.
type WikipediaSummary struct {
	Title       string
	Lang        string
	Description string // Short Wikidata description (e.g. "French politician")
	Extract     string // Plain-text intro paragraph, truncated
}

const wikiExtractMaxLen = 1500

// FetchWikipediaSummary searches Wikipedia for personName and returns the page
// summary. It tries English first then French; returns nil, nil when not found.
func FetchWikipediaSummary(ctx context.Context, httpClient *http.Client, personName string) (*WikipediaSummary, error) {
	for _, lang := range []string{"en", "fr"} {
		summary, err := fetchWikipediaSummaryForLang(ctx, httpClient, personName, lang)
		if err != nil {
			// Non-fatal: try next language
			continue
		}
		if summary != nil {
			return summary, nil
		}
	}
	return nil, nil
}

type wikiSearchResponse struct {
	Query struct {
		Search []struct {
			Title string `json:"title"`
		} `json:"search"`
	} `json:"query"`
}

type wikiSummaryResponse struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Extract     string `json:"extract"`
}

func fetchWikipediaSummaryForLang(ctx context.Context, httpClient *http.Client, personName, lang string) (*WikipediaSummary, error) {
	// Step 1: search for the page title
	searchURL := fmt.Sprintf(
		"https://%s.wikipedia.org/w/api.php?action=query&list=search&srsearch=%s&format=json&srlimit=1&srnamespace=0",
		lang, url.QueryEscape(personName),
	)
	body, err := doGet(ctx, httpClient, searchURL)
	if err != nil {
		return nil, fmt.Errorf("wikipedia search (%s): %w", lang, err)
	}

	var searchResp wikiSearchResponse
	if err := json.Unmarshal(body, &searchResp); err != nil {
		return nil, fmt.Errorf("unmarshal wikipedia search: %w", err)
	}
	if len(searchResp.Query.Search) == 0 {
		return nil, nil
	}

	pageTitle := searchResp.Query.Search[0].Title

	// Step 2: fetch the REST summary (includes description + plain-text extract)
	summaryURL := fmt.Sprintf(
		"https://%s.wikipedia.org/api/rest_v1/page/summary/%s",
		lang, url.PathEscape(pageTitle),
	)
	body, err = doGet(ctx, httpClient, summaryURL)
	if err != nil {
		return nil, fmt.Errorf("wikipedia summary (%s): %w", lang, err)
	}

	var summaryResp wikiSummaryResponse
	if err := json.Unmarshal(body, &summaryResp); err != nil {
		return nil, fmt.Errorf("unmarshal wikipedia summary: %w", err)
	}
	if summaryResp.Extract == "" {
		return nil, nil
	}

	extract := summaryResp.Extract
	if len(extract) > wikiExtractMaxLen {
		// Trim at the last sentence boundary before the limit
		trimmed := extract[:wikiExtractMaxLen]
		if idx := strings.LastIndex(trimmed, ". "); idx > 0 {
			trimmed = trimmed[:idx+1]
		}
		extract = trimmed + " [...]"
	}

	return &WikipediaSummary{
		Title:       summaryResp.Title,
		Lang:        lang,
		Description: summaryResp.Description,
		Extract:     extract,
	}, nil
}
