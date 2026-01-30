package search

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

// NewsSearchOptions configures a news search query.
type NewsSearchOptions struct {
	// DateFrom is the start of the date range (inclusive). If zero, no lower bound.
	DateFrom time.Time
	// DateTo is the end of the date range (inclusive). If zero, defaults to today.
	DateTo time.Time
	// Limit is the maximum number of results to return. SerpAPI default is 10.
	Limit int
	// Location restricts results to a specific country/region (e.g., "France", "United States").
	Location string
}

// NewsResult represents a single news article found by the search.
type NewsResult struct {
	// Title is the headline of the news article.
	Title string
	// Snippet is a brief excerpt from the article.
	Snippet string
	// Link is the URL to the full article.
	Link string
	// Source is the name of the news publication.
	Source string
	// Date is the publication date of the article, if available.
	Date *time.Time
	// StoryToken is a SerpAPI-specific identifier for the news story (useful for deduplication).
	StoryToken string
}

// NewsSearchPreview contains metadata about search results without the full results.
type NewsSearchPreview struct {
	// TotalResults is the estimated number of results available.
	TotalResults int
	// Query is the search query that was executed.
	Query string
}

// SerpAPINewsProvider implements news search using the SerpAPI Google News endpoint.
type SerpAPINewsProvider struct {
	// apiKey is the SerpAPI API key.
	apiKey string
	// httpClient is the client used to make HTTP requests.
	httpClient *http.Client
}

// serpAPIResponse defines the structure for the JSON response from SerpAPI Google News.
type serpAPIResponse struct {
	SearchMetadata struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"search_metadata"`
	SearchInformation struct {
		TotalResults int `json:"news_results_total"`
	} `json:"search_information"`
	NewsResults []struct {
		Position   int    `json:"position"`
		Title      string `json:"title"`
		Link       string `json:"link"`
		Snippet    string `json:"snippet"`
		Date       string `json:"date"`
		ISODate    string `json:"iso_date"`
		StoryToken string `json:"story_token"`
		Source     struct {
			Name    string   `json:"name"`
			Icon    string   `json:"icon"`
			Authors []string `json:"authors"`
		} `json:"source"`
	} `json:"news_results"`
	Error string `json:"error"`
}

// NewSerpAPINewsProvider creates a new SerpAPINewsProvider instance.
func NewSerpAPINewsProvider(apiKey string) (*SerpAPINewsProvider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("SerpAPI API key cannot be empty")
	}
	return &SerpAPINewsProvider{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}, nil
}

// NewSerpAPINewsProviderFromEnv creates a provider from the SERPAPI_KEY environment variable.
func NewSerpAPINewsProviderFromEnv() (*SerpAPINewsProvider, error) {
	apiKey := os.Getenv("SERPAPI_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("SERPAPI_KEY environment variable not set")
	}
	return NewSerpAPINewsProvider(apiKey)
}

// buildSearchURL constructs the SerpAPI request URL with all parameters.
func (s *SerpAPINewsProvider) buildSearchURL(query string, opts NewsSearchOptions) string {
	baseURL := "https://serpapi.com/search.json"
	params := url.Values{}
	params.Add("api_key", s.apiKey)
	params.Add("engine", "google_news")
	params.Add("q", query)

	// Add date range filter using tbs parameter
	// Format: tbs=cdr:1,cd_min:MM/DD/YYYY,cd_max:MM/DD/YYYY
	if !opts.DateFrom.IsZero() || !opts.DateTo.IsZero() {
		dateTo := opts.DateTo
		if dateTo.IsZero() {
			dateTo = time.Now()
		}
		dateFrom := opts.DateFrom
		if dateFrom.IsZero() {
			// Default to 1 year ago if not specified
			dateFrom = dateTo.AddDate(-1, 0, 0)
		}
		tbs := fmt.Sprintf("cdr:1,cd_min:%s,cd_max:%s",
			dateFrom.Format("1/2/2006"),
			dateTo.Format("1/2/2006"))
		params.Add("tbs", tbs)
	}

	// Add location filter
	if opts.Location != "" {
		params.Add("gl", mapLocationToGLCode(opts.Location))
	}

	// Add result limit
	if opts.Limit > 0 {
		params.Add("num", fmt.Sprintf("%d", opts.Limit))
	}

	return baseURL + "?" + params.Encode()
}

// mapLocationToGLCode converts a location name to a Google country code.
func mapLocationToGLCode(location string) string {
	// Common mappings - extend as needed
	locationMap := map[string]string{
		"France":        "fr",
		"United States": "us",
		"Germany":       "de",
		"United Kingdom": "uk",
		"Spain":         "es",
		"Italy":         "it",
	}
	if code, ok := locationMap[location]; ok {
		return code
	}
	// Assume it's already a country code
	return location
}

// SearchNews performs a news search for the given query.
func (s *SerpAPINewsProvider) SearchNews(query string, opts NewsSearchOptions) ([]NewsResult, error) {
	searchURL := s.buildSearchURL(query, opts)

	log.Debug().Str("query", query).Interface("opts", opts).Msg("Performing SerpAPI news search")

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create search request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SerpAPI request failed with status: %s", resp.Status)
	}

	var searchResp serpAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("failed to decode search response: %w", err)
	}

	if searchResp.Error != "" {
		return nil, fmt.Errorf("SerpAPI error: %s", searchResp.Error)
	}

	results := make([]NewsResult, 0, len(searchResp.NewsResults))
	for _, item := range searchResp.NewsResults {
		result := NewsResult{
			Title:      item.Title,
			Snippet:    item.Snippet,
			Link:       item.Link,
			Source:     item.Source.Name,
			StoryToken: item.StoryToken,
		}

		// Parse the ISO date if available
		if item.ISODate != "" {
			if t, err := time.Parse(time.RFC3339, item.ISODate); err == nil {
				result.Date = &t
			}
		}

		results = append(results, result)
	}

	log.Debug().Int("count", len(results)).Str("query", query).Msg("SerpAPI news search completed")
	return results, nil
}

// PreviewSearch performs a lightweight search to check if any results exist.
// This is useful for prioritization - skip targets with no coverage.
func (s *SerpAPINewsProvider) PreviewSearch(query string, opts NewsSearchOptions) (*NewsSearchPreview, error) {
	// Override limit to 1 for a quick check
	previewOpts := opts
	previewOpts.Limit = 1

	searchURL := s.buildSearchURL(query, previewOpts)

	log.Debug().Str("query", query).Msg("Performing SerpAPI news preview search")

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create preview request: %w", err)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute preview request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("SerpAPI preview request failed with status: %s", resp.Status)
	}

	var searchResp serpAPIResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("failed to decode preview response: %w", err)
	}

	if searchResp.Error != "" {
		return nil, fmt.Errorf("SerpAPI error: %s", searchResp.Error)
	}

	return &NewsSearchPreview{
		TotalResults: len(searchResp.NewsResults), // Use actual count since SerpAPI may not always return total
		Query:        query,
	}, nil
}

// Search implements the SearchProvider interface for backwards compatibility.
// It performs a news search and converts results to the generic SearchResult format.
func (s *SerpAPINewsProvider) Search(personName string) ([]SearchResult, error) {
	newsResults, err := s.SearchNews(personName, NewsSearchOptions{})
	if err != nil {
		return nil, err
	}

	results := make([]SearchResult, 0, len(newsResults))
	for _, nr := range newsResults {
		results = append(results, SearchResult{
			URL:     nr.Link,
			Title:   nr.Title,
			Snippet: nr.Snippet,
		})
	}
	return results, nil
}
