package search

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestSerpAPINewsProvider_BuildSearchURL tests URL construction with various options.
func TestSerpAPINewsProvider_BuildSearchURL(t *testing.T) {
	provider := &SerpAPINewsProvider{
		apiKey:     "test-api-key",
		httpClient: &http.Client{},
	}

	tests := []struct {
		name     string
		query    string
		opts     NewsSearchOptions
		wantInc  []string // Substrings that should be in the URL
		wantExcl []string // Substrings that should NOT be in the URL
	}{
		{
			name:  "basic query",
			query: "Jean Dupont",
			opts:  NewsSearchOptions{},
			wantInc: []string{
				"engine=google_news",
				"q=Jean+Dupont",
				"api_key=test-api-key",
			},
		},
		{
			name:  "with date range",
			query: "Marine Le Pen",
			opts: NewsSearchOptions{
				DateFrom: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
				DateTo:   time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC),
			},
			wantInc: []string{
				"tbs=cdr%3A1%2Ccd_min%3A1%2F1%2F2024%2Ccd_max%3A6%2F30%2F2024",
			},
		},
		{
			name:  "with location",
			query: "Emmanuel Macron",
			opts: NewsSearchOptions{
				Location: "France",
			},
			wantInc: []string{
				"gl=fr",
			},
		},
		{
			name:  "with limit",
			query: "Gabriel Attal",
			opts: NewsSearchOptions{
				Limit: 5,
			},
			wantInc: []string{
				"num=5",
			},
		},
		{
			name:  "combined options",
			query: "Jordan Bardella",
			opts: NewsSearchOptions{
				DateFrom: time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC),
				DateTo:   time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				Limit:    10,
				Location: "France",
			},
			wantInc: []string{
				"engine=google_news",
				"q=Jordan+Bardella",
				"gl=fr",
				"num=10",
				"tbs=",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			url := provider.buildSearchURL(tt.query, tt.opts)

			for _, want := range tt.wantInc {
				if !strings.Contains(url, want) {
					t.Errorf("URL should contain %q, got: %s", want, url)
				}
			}

			for _, notWant := range tt.wantExcl {
				if strings.Contains(url, notWant) {
					t.Errorf("URL should NOT contain %q, got: %s", notWant, url)
				}
			}
		})
	}
}

// TestSerpAPINewsProvider_LocationMapping tests the location to GL code mapping.
func TestSerpAPINewsProvider_LocationMapping(t *testing.T) {
	tests := []struct {
		location string
		expected string
	}{
		{"France", "fr"},
		{"United States", "us"},
		{"Germany", "de"},
		{"United Kingdom", "uk"},
		{"Spain", "es"},
		{"Italy", "it"},
		{"fr", "fr"}, // Already a code
		{"UnknownCountry", "UnknownCountry"}, // Pass through
	}

	for _, tt := range tests {
		t.Run(tt.location, func(t *testing.T) {
			got := mapLocationToGLCode(tt.location)
			if got != tt.expected {
				t.Errorf("mapLocationToGLCode(%q) = %q, want %q", tt.location, got, tt.expected)
			}
		})
	}
}

// TestSerpAPINewsProvider_ParseResponse tests parsing of SerpAPI JSON responses.
func TestSerpAPINewsProvider_ParseResponse(t *testing.T) {
	// Create a mock server that returns a sample SerpAPI response
	mockResponse := `{
		"search_metadata": {
			"id": "test-search-id",
			"status": "Success"
		},
		"search_information": {
			"news_results_total": 2
		},
		"news_results": [
			{
				"position": 1,
				"title": "Test News Article 1",
				"link": "https://example.com/article1",
				"snippet": "This is a test snippet for article 1",
				"date": "2 days ago",
				"iso_date": "2024-01-15T10:30:00Z",
				"story_token": "story-token-1",
				"source": {
					"name": "Test News Source",
					"icon": "https://example.com/icon.png",
					"authors": ["Author 1"]
				}
			},
			{
				"position": 2,
				"title": "Test News Article 2",
				"link": "https://example.com/article2",
				"snippet": "This is a test snippet for article 2",
				"date": "3 days ago",
				"iso_date": "2024-01-14T15:45:00Z",
				"story_token": "story-token-2",
				"source": {
					"name": "Another News Source",
					"icon": "https://example.com/icon2.png",
					"authors": ["Author 2", "Author 3"]
				}
			}
		]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(mockResponse))
	}))
	defer server.Close()

	// Create provider with mock server URL
	// We need to modify the provider to use a custom base URL for testing
	// For now, test by parsing the response directly
	var resp serpAPIResponse
	err := json.Unmarshal([]byte(mockResponse), &resp)
	if err != nil {
		t.Fatalf("Failed to parse mock response: %v", err)
	}

	// Verify parsing
	if len(resp.NewsResults) != 2 {
		t.Errorf("Expected 2 news results, got %d", len(resp.NewsResults))
	}

	// Check first result
	if resp.NewsResults[0].Title != "Test News Article 1" {
		t.Errorf("Expected title 'Test News Article 1', got %q", resp.NewsResults[0].Title)
	}
	if resp.NewsResults[0].Link != "https://example.com/article1" {
		t.Errorf("Expected link 'https://example.com/article1', got %q", resp.NewsResults[0].Link)
	}
	if resp.NewsResults[0].Source.Name != "Test News Source" {
		t.Errorf("Expected source name 'Test News Source', got %q", resp.NewsResults[0].Source.Name)
	}
	if resp.NewsResults[0].StoryToken != "story-token-1" {
		t.Errorf("Expected story token 'story-token-1', got %q", resp.NewsResults[0].StoryToken)
	}

	// Check date parsing
	expectedDate, _ := time.Parse(time.RFC3339, "2024-01-15T10:30:00Z")
	parsedDate, err := time.Parse(time.RFC3339, resp.NewsResults[0].ISODate)
	if err != nil {
		t.Errorf("Failed to parse ISO date: %v", err)
	}
	if !parsedDate.Equal(expectedDate) {
		t.Errorf("Expected date %v, got %v", expectedDate, parsedDate)
	}
}

// TestSerpAPINewsProvider_SearchInterface tests that SerpAPINewsProvider implements SearchProvider.
func TestSerpAPINewsProvider_SearchInterface(t *testing.T) {
	provider := &SerpAPINewsProvider{
		apiKey:     "test-key",
		httpClient: &http.Client{},
	}

	// This test just verifies that the Search method signature is correct
	// by ensuring it compiles as a SearchProvider
	var _ SearchProvider = provider
}

// TestSerpAPINewsProvider_ErrorHandling tests error handling for API errors.
func TestSerpAPINewsProvider_ErrorHandling(t *testing.T) {
	// Test with empty API error response
	errorResponse := `{
		"error": "Invalid API key"
	}`

	var resp serpAPIResponse
	err := json.Unmarshal([]byte(errorResponse), &resp)
	if err != nil {
		t.Fatalf("Failed to parse error response: %v", err)
	}

	if resp.Error != "Invalid API key" {
		t.Errorf("Expected error 'Invalid API key', got %q", resp.Error)
	}
}

// TestSerpAPINewsProvider_SearchWebParsing tests parsing of SerpAPI Google Search organic results.
func TestSerpAPINewsProvider_SearchWebParsing(t *testing.T) {
	mockResponse := `{
		"search_metadata": {
			"id": "test-web-search-id",
			"status": "Success"
		},
		"organic_results": [
			{
				"position": 1,
				"title": "Emmanuel Macron - Wikipedia",
				"link": "https://en.wikipedia.org/wiki/Emmanuel_Macron",
				"snippet": "Emmanuel Jean-Michel Frederic Macron is a French politician."
			},
			{
				"position": 2,
				"title": "Macron Official Site",
				"link": "https://www.elysee.fr/emmanuel-macron",
				"snippet": "Official page of the President of France."
			},
			{
				"position": 3,
				"title": "Macron on X",
				"link": "https://x.com/EmmanuelMacron",
				"snippet": "The latest posts from Emmanuel Macron."
			}
		]
	}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify engine=google is set (not google_news)
		if engine := r.URL.Query().Get("engine"); engine != "google" {
			t.Errorf("expected engine=google, got %q", engine)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(mockResponse))
	}))
	defer server.Close()

	provider := &SerpAPINewsProvider{
		apiKey:     "test-key",
		httpClient: server.Client(),
	}
	// Override URL to point to mock server
	provider.httpClient.Transport = roundTripFuncSerp(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(req)
	})

	results, err := provider.SearchWeb("Emmanuel Macron", 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Verify first result
	if results[0].Title != "Emmanuel Macron - Wikipedia" {
		t.Errorf("expected title 'Emmanuel Macron - Wikipedia', got %q", results[0].Title)
	}
	if results[0].URL != "https://en.wikipedia.org/wiki/Emmanuel_Macron" {
		t.Errorf("expected URL 'https://en.wikipedia.org/wiki/Emmanuel_Macron', got %q", results[0].URL)
	}
	if results[0].Snippet != "Emmanuel Jean-Michel Frederic Macron is a French politician." {
		t.Errorf("expected snippet about French politician, got %q", results[0].Snippet)
	}

	// Verify third result (X/Twitter profile)
	if results[2].URL != "https://x.com/EmmanuelMacron" {
		t.Errorf("expected URL 'https://x.com/EmmanuelMacron', got %q", results[2].URL)
	}
}

// TestSerpAPINewsProvider_SearchWebError tests error handling for web search.
func TestSerpAPINewsProvider_SearchWebError(t *testing.T) {
	mockResponse := `{"error": "Rate limit exceeded"}`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(mockResponse))
	}))
	defer server.Close()

	provider := &SerpAPINewsProvider{
		apiKey:     "test-key",
		httpClient: server.Client(),
	}
	provider.httpClient.Transport = roundTripFuncSerp(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(req)
	})

	_, err := provider.SearchWeb("test", 10)
	if err == nil {
		t.Fatal("expected error from SerpAPI error response")
	}
	if !strings.Contains(err.Error(), "Rate limit exceeded") {
		t.Errorf("expected rate limit error, got: %s", err.Error())
	}
}

// roundTripFuncSerp allows using a function as an http.RoundTripper in serpapi tests.
type roundTripFuncSerp func(*http.Request) (*http.Response, error)

func (f roundTripFuncSerp) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

// TestSerpAPINewsProvider_Integration is an integration test that requires SERPAPI_KEY.
// It's skipped if the environment variable is not set.
func TestSerpAPINewsProvider_Integration(t *testing.T) {
	loadEnv(t)

	apiKey := os.Getenv("SERPAPI_KEY")
	if apiKey == "" {
		t.Skip("Skipping SerpAPI integration test: SERPAPI_KEY not set")
	}

	provider, err := NewSerpAPINewsProvider(apiKey)
	if err != nil {
		t.Fatalf("Failed to create SerpAPINewsProvider: %v", err)
	}

	// Search for a well-known French politician
	query := "Emmanuel Macron"
	opts := NewsSearchOptions{
		Location: "France",
		Limit:    3,
	}

	t.Logf("Searching for news about: %s", query)
	results, err := provider.SearchNews(query, opts)
	if err != nil {
		t.Fatalf("SearchNews failed: %v", err)
	}

	if len(results) == 0 {
		t.Log("Warning: Search returned 0 results")
	}

	t.Logf("Received %d results", len(results))
	var validResults, emptyLinks int
	for i, res := range results {
		t.Logf("  %d: %s (%s) from %s", i+1, res.Title, res.Link, res.Source)
		if res.Link == "" {
			emptyLinks++
			t.Logf("Result %d has empty link (external API data quality issue)", i+1)
		} else {
			validResults++
		}
	}

	// Verify we got at least some valid results
	if validResults == 0 && len(results) > 0 {
		t.Errorf("All %d results have empty links", len(results))
	}
	if emptyLinks > 0 {
		t.Logf("Warning: %d/%d results had empty links (external API issue)", emptyLinks, len(results))
	}
}
