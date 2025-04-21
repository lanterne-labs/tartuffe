package search

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"

	"github.com/rs/zerolog/log" // Import zerolog's global logger
)

// GoogleSearchProvider implements the SearchProvider interface using the Google Custom Search JSON API.
type GoogleSearchProvider struct {
	// apiKey is the Google API key for accessing the Custom Search API.
	apiKey string
	// cseID is the Custom Search Engine ID to use for the search.
	cseID string
	// httpClient is the client used to make HTTP requests.
	httpClient *http.Client
}

// NewGoogleSearchProvider creates a new GoogleSearchProvider instance.
// It requires the Google API key and Custom Search Engine ID.
// It returns an error if either key or ID is missing.
func NewGoogleSearchProvider(apiKey, cseID string) (*GoogleSearchProvider, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("Google API key cannot be empty")
	}
	if cseID == "" {
		return nil, fmt.Errorf("Google CSE ID cannot be empty")
	}
	return &GoogleSearchProvider{
		apiKey:     apiKey,
		cseID:      cseID,
		httpClient: &http.Client{}, // Use a default client
	}, nil
}

// googleSearchResponse defines the structure for the relevant parts of the JSON response from the Google API.
// We only care about the 'items' array which contains the search results.
type googleSearchResponse struct {
	// Items is a list of search result items.
	Items []struct {
		// Title is the title of the search result.
		Title string `json:"title"`
		// Link is the URL of the search result.
		Link string `json:"link"`
		// Snippet is a short description of the result.
		Snippet string `json:"snippet"`
	} `json:"items"`
	// Error contains details if the API returned an error.
	Error *struct {
		// Code is the HTTP status code of the error.
		Code int `json:"code"`
		// Message is a description of the error.
		Message string `json:"message"`
	} `json:"error"`
}

// Search performs a web search using the original method signature, calling SearchWithOptions with nil options.
func (g *GoogleSearchProvider) Search(personName string) ([]SearchResult, error) {
	return g.SearchWithOptions(personName, nil)
}

// SearchWithOptions performs a web search for the given personName using the Google Custom Search API.
// It accepts an optional map of query parameters (e.g., {"num": "5"} to limit results).
// It returns a slice of SearchResult structs containing the URLs and other details.
func (g *GoogleSearchProvider) SearchWithOptions(personName string, options map[string]string) ([]SearchResult, error) {
	baseURL := "https://www.googleapis.com/customsearch/v1"
	params := url.Values{}
	params.Add("key", g.apiKey)
	params.Add("cx", g.cseID)
	params.Add("q", personName)

	// Add optional parameters
	if options != nil {
		for key, value := range options {
			params.Add(key, value)
		}
	}

	searchURL := baseURL + "?" + params.Encode()
	// Avoid logging the full searchURL which contains the API key
	log.Debug().Str("query", personName).Str("baseURL", baseURL).Interface("options", options).Msg("Performing Google Custom Search")

	req, err := http.NewRequest("GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create search request: %w", err)
	}

	resp, err := g.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute search request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Try to parse error from response body
		var apiError googleSearchResponse
		if err := json.NewDecoder(resp.Body).Decode(&apiError); err == nil && apiError.Error != nil {
			return nil, fmt.Errorf("Google Search API error (%d): %s", apiError.Error.Code, apiError.Error.Message)
		}
		return nil, fmt.Errorf("Google Search API request failed with status: %s", resp.Status)
	}

	var searchResp googleSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("failed to decode search response: %w", err)
	}

	if searchResp.Error != nil {
		return nil, fmt.Errorf("Google Search API returned error (%d): %s", searchResp.Error.Code, searchResp.Error.Message)
	}

	results := make([]SearchResult, 0, len(searchResp.Items))
	for _, item := range searchResp.Items {
		results = append(results, SearchResult{
			URL:     item.Link,
			Title:   item.Title,
			Snippet: item.Snippet,
		})
	}
	log.Debug().Int("count", len(results)).Str("query", personName).Msg("Google search completed")
	return results, nil
}

// Helper function to create a provider from environment variables.
// This is convenient for initializing the provider in main.go or tests.
func NewGoogleSearchProviderFromEnv() (*GoogleSearchProvider, error) {
	apiKey := os.Getenv("GOOGLE_API_KEY")
	cseID := os.Getenv("GOOGLE_CSE_ID")
	
	if apiKey == "" {
		return nil, fmt.Errorf("GOOGLE_API_KEY environment variable not set")
	}
	if cseID == "" {
		return nil, fmt.Errorf("GOOGLE_CSE_ID environment variable not set")
	}

	return NewGoogleSearchProvider(apiKey, cseID)
} 