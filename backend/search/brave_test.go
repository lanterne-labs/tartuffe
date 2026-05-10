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

func TestBraveNewsResponseParsing(t *testing.T) {
	response := braveNewsResponse{
		Results: []braveNewsResult{
			{
				Title:       "Test Article",
				URL:         "https://example.com/article",
				Description: "A test article snippet",
				PageAge:     "2024-03-15T10:00:00Z",
				MetaURL:     struct{ Hostname string `json:"hostname"` }{Hostname: "example.com"},
			},
			{
				Title:       "Second Article",
				URL:         "https://news.example.com/story",
				Description: "Another snippet",
				PageAge:     "2024-03-14",
			},
		},
		Count: 2,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Subscription-Token") != "test-key" {
			t.Errorf("expected X-Subscription-Token=test-key, got %s", r.Header.Get("X-Subscription-Token"))
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewBraveSearchProvider("test-key")
	provider.httpClient = server.Client()

	// Override the URL by using a custom transport
	originalFetch := provider.httpClient.Transport
	provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		if originalFetch != nil {
			return originalFetch.RoundTrip(req)
		}
		return http.DefaultTransport.RoundTrip(req)
	})

	results, err := provider.SearchNews("test query", NewsSearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	if results[0].Title != "Test Article" {
		t.Errorf("expected title 'Test Article', got %q", results[0].Title)
	}
	if results[0].Link != "https://example.com/article" {
		t.Errorf("expected link 'https://example.com/article', got %q", results[0].Link)
	}
	if results[0].Snippet != "A test article snippet" {
		t.Errorf("expected snippet 'A test article snippet', got %q", results[0].Snippet)
	}
	if results[0].Source != "example.com" {
		t.Errorf("expected source 'example.com', got %q", results[0].Source)
	}
	if results[0].Date == nil {
		t.Error("expected date to be parsed")
	}
	if results[0].StoryToken != "" {
		t.Errorf("expected empty story token, got %q", results[0].StoryToken)
	}

	// Second result uses date-only format
	if results[1].Date == nil {
		t.Error("expected date-only format to be parsed")
	}
}

func TestBraveFreshnessParam(t *testing.T) {
	tests := []struct {
		name     string
		from     time.Time
		to       time.Time
		expected string
	}{
		{
			name:     "both dates",
			from:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			to:       time.Date(2024, 3, 15, 0, 0, 0, 0, time.UTC),
			expected: "2024-01-01to2024-03-15",
		},
		{
			name:     "no dates",
			expected: "",
		},
		{
			name:     "only from",
			from:     time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
			expected: "2024-01-01to",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := buildFreshness(tt.from, tt.to)
			if tt.name == "only from" {
				// DateTo defaults to now, so just check the prefix
				prefix := "2024-01-01to"
				if len(result) < len(prefix) || result[:len(prefix)] != prefix {
					t.Errorf("expected prefix %q, got %q", prefix, result)
				}
			} else {
				if result != tt.expected {
					t.Errorf("expected %q, got %q", tt.expected, result)
				}
			}
		})
	}
}

func TestBravePagination(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		offset := r.URL.Query().Get("offset")

		var results []braveNewsResult
		if offset == "" || offset == "0" {
			// First page: return 20 results
			for i := 0; i < 20; i++ {
				results = append(results, braveNewsResult{
					Title: "Article",
					URL:   "https://example.com/article",
				})
			}
		} else {
			// Second page: return 5 results
			for i := 0; i < 5; i++ {
				results = append(results, braveNewsResult{
					Title: "Article page 2",
					URL:   "https://example.com/article-p2",
				})
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(braveNewsResponse{Results: results, Count: len(results)})
	}))
	defer server.Close()

	provider := NewBraveSearchProvider("test-key")
	provider.httpClient = server.Client()
	provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(req)
	})

	results, err := provider.SearchNews("test", NewsSearchOptions{Limit: 25})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if callCount != 2 {
		t.Errorf("expected 2 API calls for pagination, got %d", callCount)
	}

	if len(results) != 25 {
		t.Errorf("expected 25 results, got %d", len(results))
	}
}

func TestBraveImageResponseParsing(t *testing.T) {
	response := braveImageResponse{
		Results: []braveImageResult{
			{
				Title:  "Photo of Person",
				URL:    "https://example.com/page",
				Source: "example.com",
				Thumbnail: struct {
					Src string `json:"src"`
				}{Src: "https://example.com/thumb.jpg"},
				Properties: struct {
					URL string `json:"url"`
				}{URL: "https://example.com/full.jpg"},
			},
		},
		Count: 1,
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewBraveSearchProvider("test-key")
	provider.httpClient = server.Client()
	provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(req)
	})

	results, err := provider.SearchImages("test person", ImageSearchOptions{Limit: 10})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	img := results[0]
	if img.Original != "https://example.com/full.jpg" {
		t.Errorf("expected original URL from properties.url, got %q", img.Original)
	}
	if img.Thumbnail != "https://example.com/thumb.jpg" {
		t.Errorf("expected thumbnail, got %q", img.Thumbnail)
	}
	if img.OriginalWidth != 0 || img.OriginalHeight != 0 {
		t.Errorf("expected 0 dimensions from Brave, got %dx%d", img.OriginalWidth, img.OriginalHeight)
	}
	if img.Source != "example.com" {
		t.Errorf("expected source 'example.com', got %q", img.Source)
	}
	if img.Position != 0 {
		t.Errorf("expected position 0, got %d", img.Position)
	}
}

func TestFactoryEnvPriority(t *testing.T) {
	// Save and restore env
	origBrave := os.Getenv("BRAVE_SEARCH_API_KEY")
	origSerp := os.Getenv("SERPAPI_KEY")
	defer func() {
		os.Setenv("BRAVE_SEARCH_API_KEY", origBrave)
		os.Setenv("SERPAPI_KEY", origSerp)
	}()

	// Neither set → error
	os.Setenv("BRAVE_SEARCH_API_KEY", "")
	os.Setenv("SERPAPI_KEY", "")
	_, err := NewNewsSearcherFromEnv()
	if err == nil {
		t.Error("expected error when no API keys set")
	}

	// Only SERPAPI_KEY → SerpAPI
	os.Setenv("SERPAPI_KEY", "serp-key")
	searcher, err := NewNewsSearcherFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := searcher.(*SerpAPINewsProvider); !ok {
		t.Errorf("expected SerpAPINewsProvider, got %T", searcher)
	}

	// Both set → fallback wrapper (Brave primary, SerpAPI fallback)
	os.Setenv("BRAVE_SEARCH_API_KEY", "brave-key")
	searcher, err = NewNewsSearcherFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := searcher.(*fallbackNewsSearcher); !ok {
		t.Errorf("expected fallbackNewsSearcher, got %T", searcher)
	}

	// Image searcher same priority
	imgSearcher, err := NewImageSearcherFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := imgSearcher.(*BraveSearchProvider); !ok {
		t.Errorf("expected BraveSearchProvider for images, got %T", imgSearcher)
	}
}

func resetBraveCircuitBreaker() {
	braveCircuitMu.Lock()
	braveConsecutive429 = 0
	braveCircuitOpenUntil = time.Time{}
	braveCircuitMu.Unlock()
}

func TestBraveCircuitBreakerOpensAfterThreshold(t *testing.T) {
	resetBraveCircuitBreaker()
	defer resetBraveCircuitBreaker()

	requestCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestCount++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	provider := NewBraveSearchProvider("test-key")
	provider.httpClient = server.Client()
	provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(req)
	})

	// First call: hits the server (4 retries), gets 429s, should increment counter
	_, err := provider.SearchWeb("test", 1)
	if err == nil {
		t.Fatal("expected error from 429 response")
	}

	// The 4 retries should have pushed consecutive429 >= threshold (3)
	braveCircuitMu.Lock()
	count := braveConsecutive429
	isOpen := time.Now().Before(braveCircuitOpenUntil)
	braveCircuitMu.Unlock()

	if count < braveCircuitThreshold {
		t.Errorf("expected consecutive 429 count >= %d, got %d", braveCircuitThreshold, count)
	}
	if !isOpen {
		t.Error("expected circuit breaker to be open")
	}

	// Second call should be rejected immediately without hitting the server
	prevCount := requestCount
	_, err = provider.SearchWeb("test2", 1)
	if err == nil {
		t.Fatal("expected error from circuit breaker")
	}
	if requestCount != prevCount {
		t.Errorf("expected no new server requests when circuit is open, got %d new requests", requestCount-prevCount)
	}
	if !strings.Contains(err.Error(), "circuit breaker open") {
		t.Errorf("expected circuit breaker error message, got: %s", err.Error())
	}
}

func TestBraveCircuitBreakerResetsOnSuccess(t *testing.T) {
	resetBraveCircuitBreaker()
	defer resetBraveCircuitBreaker()

	// Manually set circuit state to near threshold
	braveCircuitMu.Lock()
	braveConsecutive429 = braveCircuitThreshold - 1
	braveCircuitMu.Unlock()

	// Server returns 200
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(struct {
			Web struct {
				Results []struct{} `json:"results"`
			} `json:"web"`
		}{})
	}))
	defer server.Close()

	provider := NewBraveSearchProvider("test-key")
	provider.httpClient = server.Client()
	provider.httpClient.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		req.URL.Scheme = "http"
		req.URL.Host = server.Listener.Addr().String()
		return http.DefaultTransport.RoundTrip(req)
	})

	_, err := provider.SearchWeb("test", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Counter should be reset to 0
	braveCircuitMu.Lock()
	count := braveConsecutive429
	braveCircuitMu.Unlock()

	if count != 0 {
		t.Errorf("expected consecutive 429 count to be reset to 0, got %d", count)
	}
}

func TestWebSearcherFromEnvFallback(t *testing.T) {
	origBrave := os.Getenv("BRAVE_SEARCH_API_KEY")
	origSerp := os.Getenv("SERPAPI_KEY")
	defer func() {
		os.Setenv("BRAVE_SEARCH_API_KEY", origBrave)
		os.Setenv("SERPAPI_KEY", origSerp)
	}()

	// Neither set -> error
	os.Setenv("BRAVE_SEARCH_API_KEY", "")
	os.Setenv("SERPAPI_KEY", "")
	_, err := NewWebSearcherFromEnv()
	if err == nil {
		t.Error("expected error when no API keys set")
	}

	// Only SERPAPI_KEY -> SerpAPI
	os.Setenv("SERPAPI_KEY", "serp-key")
	searcher, err := NewWebSearcherFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := searcher.(*SerpAPINewsProvider); !ok {
		t.Errorf("expected SerpAPINewsProvider for web search fallback, got %T", searcher)
	}

	// Both set -> fallback wrapper (Brave primary, SerpAPI fallback)
	os.Setenv("BRAVE_SEARCH_API_KEY", "brave-key")
	searcher, err = NewWebSearcherFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, ok := searcher.(*fallbackWebSearcher); !ok {
		t.Errorf("expected fallbackWebSearcher, got %T", searcher)
	}
}

// roundTripFunc allows using a function as an http.RoundTripper.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
