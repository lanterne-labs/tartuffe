package search

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// braveRateLimiter is a package-level rate limiter shared across all braveSearchProvider
// instances. Brave free tier allows 1 req/sec globally (not per-instance).
var (
	braveRateMu   sync.Mutex
	braveLastCall time.Time
)

// braveCircuitBreaker is a package-level circuit breaker for Brave API 429 responses.
// After braveCircuitThreshold consecutive 429s, the circuit opens and all requests
// are rejected immediately until the cooldown expires.
var (
	braveCircuitMu        sync.Mutex
	braveConsecutive429   int
	braveCircuitOpenUntil time.Time
)

const braveCircuitThreshold = 3            // open after 3 consecutive 429s
const braveCircuitCooldown = 1 * time.Hour // cooldown period before retrying

// braveRateLimit ensures at least 1 second between Brave API calls across all instances.
func braveRateLimit() {
	braveRateMu.Lock()
	defer braveRateMu.Unlock()
	if elapsed := time.Since(braveLastCall); elapsed < time.Second {
		time.Sleep(time.Second - elapsed)
	}
	braveLastCall = time.Now()
}

const braveMaxRetries = 4

// doBraveRequest executes an HTTP request against the Brave API with rate limiting
// and exponential backoff on 429 responses. Returns the response body on success.
func (b *braveSearchProvider) doBraveRequest(reqURL, apiType string) (*http.Response, error) {
	// Check circuit breaker before entering the retry loop
	braveCircuitMu.Lock()
	if time.Now().Before(braveCircuitOpenUntil) {
		retryAfter := braveCircuitOpenUntil
		braveCircuitMu.Unlock()
		return nil, fmt.Errorf("%w: Brave API circuit breaker open, retry after %s", ErrQuota, retryAfter.Format(time.RFC3339))
	}
	braveCircuitMu.Unlock()

	backoff := 2 * time.Second

	for attempt := range braveMaxRetries {
		braveRateLimit()

		req, err := http.NewRequest("GET", reqURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create Brave %s request: %w", apiType, err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Subscription-Token", b.apiKey)

		resp, err := b.httpClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("Brave %s request failed: %w", apiType, err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			resp.Body.Close()

			// Update circuit breaker state
			braveCircuitMu.Lock()
			braveConsecutive429++
			if braveConsecutive429 >= braveCircuitThreshold {
				braveCircuitOpenUntil = time.Now().Add(braveCircuitCooldown)
				log.Warn().
					Int("consecutive_429s", braveConsecutive429).
					Time("circuit_open_until", braveCircuitOpenUntil).
					Msg("Brave API circuit breaker opened (quota exhausted)")
			}
			braveCircuitMu.Unlock()

			if attempt < braveMaxRetries-1 {
				log.Warn().Str("api", apiType).Int("attempt", attempt+1).Dur("backoff", backoff).Msg("Brave API 429, backing off")
				time.Sleep(backoff)
				backoff *= 2
				continue
			}
			return nil, fmt.Errorf("%w: Brave %s API returned status 429 Too Many Requests after %d retries", ErrQuota, apiType, braveMaxRetries)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("Brave %s API returned status %s", apiType, resp.Status)
		}

		// Success: reset circuit breaker counter
		braveCircuitMu.Lock()
		braveConsecutive429 = 0
		braveCircuitMu.Unlock()

		return resp, nil
	}

	return nil, fmt.Errorf("Brave %s API: exhausted retries", apiType)
}

// braveSearchProvider implements NewsSearcher, WebSearcher and ImageSearcher
// using the Brave Search API.
type braveSearchProvider struct {
	apiKey     string
	httpClient *http.Client
}

// newBraveSearchProvider creates a new braveSearchProvider instance.
func newBraveSearchProvider(apiKey string) *braveSearchProvider {
	return &braveSearchProvider{
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

const braveMaxPerPage = 20

// braveNewsResponse is the JSON structure returned by the Brave News Search API.
type braveNewsResponse struct {
	Query struct {
		Original string `json:"original"`
	} `json:"query"`
	Results []braveNewsResult `json:"results"`
	Count   int               `json:"count"`
}

type braveNewsResult struct {
	Title       string `json:"title"`
	URL         string `json:"url"`
	Description string `json:"description"`
	PageAge     string `json:"page_age"`
	MetaURL     struct {
		Hostname string `json:"hostname"`
	} `json:"meta_url"`
}

// SearchNews performs a news search using the Brave News Search API.
func (b *braveSearchProvider) SearchNews(query string, opts NewsSearchOptions) ([]NewsResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}

	var allResults []NewsResult

	for offset := 0; offset < limit; offset += braveMaxPerPage {
		count := braveMaxPerPage
		if remaining := limit - offset; remaining < count {
			count = remaining
		}

		results, err := b.fetchNewsPage(query, opts, count, offset)
		if err != nil {
			return nil, err
		}

		allResults = append(allResults, results...)

		// If we got fewer results than requested, no more pages
		if len(results) < count {
			break
		}
	}

	log.Debug().Int("count", len(allResults)).Str("query", query).Msg("Brave news search completed")
	return allResults, nil
}

func (b *braveSearchProvider) fetchNewsPage(query string, opts NewsSearchOptions, count, offset int) ([]NewsResult, error) {
	params := url.Values{}
	params.Add("q", query)
	params.Add("count", fmt.Sprintf("%d", count))
	if offset > 0 {
		params.Add("offset", fmt.Sprintf("%d", offset))
	}

	// Country filter
	country := ""
	if opts.Location != "" {
		country = mapLocationToGLCode(opts.Location)
	}
	if opts.Language != "" {
		// Brave: search_lang (results in this language) + optional ui_lang
		// (interface). search_lang is what actually constrains content.
		params.Add("search_lang", opts.Language)
		params.Add("ui_lang", opts.Language)
		if country == "" {
			country = langToCountryGL(opts.Language)
		}
	}
	if country != "" {
		params.Add("country", country)
	}

	// Freshness / date range
	if freshness := buildFreshness(opts.DateFrom, opts.DateTo); freshness != "" {
		params.Add("freshness", freshness)
	}

	reqURL := "https://api.search.brave.com/res/v1/news/search?" + params.Encode()

	log.Debug().Str("query", query).Int("count", count).Int("offset", offset).Msg("Brave news search request")

	resp, err := b.doBraveRequest(reqURL, "news")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var braveResp braveNewsResponse
	if err := json.NewDecoder(resp.Body).Decode(&braveResp); err != nil {
		return nil, fmt.Errorf("failed to decode Brave news response: %w", err)
	}

	results := make([]NewsResult, 0, len(braveResp.Results))
	for _, item := range braveResp.Results {
		r := NewsResult{
			Title:   item.Title,
			Link:    item.URL,
			Snippet: item.Description,
			Source:  item.MetaURL.Hostname,
		}

		if item.PageAge != "" {
			if t, err := parsePageAge(item.PageAge); err == nil {
				r.Date = &t
			}
		}

		results = append(results, r)
	}

	return results, nil
}

// buildFreshness converts DateFrom/DateTo into Brave's freshness parameter format.
// Format: "YYYY-MM-DDtoYYYY-MM-DD"
func buildFreshness(dateFrom, dateTo time.Time) string {
	if dateFrom.IsZero() && dateTo.IsZero() {
		return ""
	}
	if dateTo.IsZero() {
		dateTo = time.Now()
	}
	if dateFrom.IsZero() {
		dateFrom = dateTo.AddDate(-1, 0, 0)
	}
	return dateFrom.Format("2006-01-02") + "to" + dateTo.Format("2006-01-02")
}

// parsePageAge tries multiple formats for the page_age field.
func parsePageAge(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unable to parse page_age %q", s)
}

// braveImageResponse is the JSON structure returned by the Brave Image Search API.
type braveImageResponse struct {
	Results []braveImageResult `json:"results"`
	Count   int                `json:"count"`
}

type braveImageResult struct {
	Title     string `json:"title"`
	URL       string `json:"url"`
	Source    string `json:"source"`
	Thumbnail struct {
		Src string `json:"src"`
	} `json:"thumbnail"`
	Properties struct {
		URL string `json:"url"`
	} `json:"properties"`
}

// SearchImages performs an image search using the Brave Image Search API.
func (b *braveSearchProvider) SearchImages(query string, opts ImageSearchOptions) ([]ImageResult, error) {
	params := url.Values{}
	params.Add("q", query)

	limit := opts.Limit
	if limit <= 0 {
		limit = 10
	}
	params.Add("count", fmt.Sprintf("%d", limit))

	if opts.Location != "" {
		params.Add("country", mapLocationToGLCode(opts.Location))
	}

	reqURL := "https://api.search.brave.com/res/v1/images/search?" + params.Encode()

	log.Debug().Str("query", query).Int("limit", limit).Msg("Brave image search request")

	resp, err := b.doBraveRequest(reqURL, "image")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var braveResp braveImageResponse
	if err := json.NewDecoder(resp.Body).Decode(&braveResp); err != nil {
		return nil, fmt.Errorf("failed to decode Brave image response: %w", err)
	}

	results := make([]ImageResult, 0, len(braveResp.Results))
	for i, item := range braveResp.Results {
		original := item.Properties.URL
		if original == "" {
			original = item.URL
		}

		results = append(results, ImageResult{
			Original:       original,
			Thumbnail:      item.Thumbnail.Src,
			OriginalWidth:  0, // Brave doesn't provide dimensions
			OriginalHeight: 0,
			Source:         item.Source,
			Title:          item.Title,
			Position:       i,
		})
	}

	log.Debug().Int("count", len(results)).Str("query", query).Msg("Brave image search completed")
	return results, nil
}

// SearchWeb performs a general web search using the Brave Web Search API.
func (b *braveSearchProvider) SearchWeb(query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 10
	}

	params := url.Values{}
	params.Add("q", query)
	params.Add("count", fmt.Sprintf("%d", limit))

	reqURL := "https://api.search.brave.com/res/v1/web/search?" + params.Encode()

	log.Debug().Str("query", query).Int("limit", limit).Msg("Brave web search request")

	resp, err := b.doBraveRequest(reqURL, "web")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var braveResp struct {
		Web struct {
			Results []struct {
				Title       string `json:"title"`
				URL         string `json:"url"`
				Description string `json:"description"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&braveResp); err != nil {
		return nil, fmt.Errorf("failed to decode Brave web response: %w", err)
	}

	results := make([]SearchResult, 0, len(braveResp.Web.Results))
	for _, item := range braveResp.Web.Results {
		results = append(results, SearchResult{
			URL:     item.URL,
			Title:   item.Title,
			Snippet: item.Description,
		})
	}

	log.Debug().Int("count", len(results)).Str("query", query).Msg("Brave web search completed")
	return results, nil
}
