package content

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"
)

// ApifyExtractor implements the ContentExtractor interface using the Apify API.
type ApifyExtractor struct {
	// apiKey is the Apify API token.
	apiKey string
	// actorID is the ID of the Apify actor to use (e.g., "apify/website-content-crawler").
	actorID string
	// httpClient is used for making requests to the Apify API.
	httpClient *http.Client
}

// NewApifyExtractor creates a new ApifyExtractor instance.
// It requires the Apify API key.
func NewApifyExtractor(apiKey string) (*ApifyExtractor, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("Apify API key cannot be empty")
	}
	return &ApifyExtractor{
		apiKey:     apiKey,
		actorID:    "apify/website-content-crawler", // Use the specific content crawler actor
		httpClient: &http.Client{Timeout: 180 * time.Second}, // Increase timeout slightly for potentially longer crawls
	}, nil
}

// NewApifyExtractorFromEnv creates a new ApifyExtractor using the API key from the APIFY_API_KEY environment variable.
func NewApifyExtractorFromEnv() (*ApifyExtractor, error) {
	apiKey := os.Getenv("APIFY_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("APIFY_API_KEY environment variable not set")
	}
	return NewApifyExtractor(apiKey)
}

// Define structs for Apify API request and response
// Based on Apify docs for running an actor synchronously:
// https://docs.apify.com/api/v2#/reference/actors/run-actor-synchronously/run-actor-synchronously-and-get-dataset-items

type apifyRunInput struct {
	// StartUrls are the URLs to scrape.
	StartURLs []struct {
		URL string `json:"url"`
	} `json:"startUrls"`
	// Add specific input fields for website-content-crawler if needed
	// e.g., to request Markdown output: "outputFormat": "markdown"
	// For now, rely on actor defaults (likely plain text)
}

// apifyRunSyncResponse represents the structure returned by the /run-sync endpoint.
// It contains details about the actor run, including the ID of the default dataset.
type apifyRunSyncResponse struct {
	// Data contains the details of the finished actor run.
	Data struct {
		// ID is the unique identifier of the actor run.
		ID string `json:"id"`
		// ActorID is the ID of the actor that was run.
		ActorID string `json:"actId"`
		// Status indicates the final status of the run (e.g., SUCCEEDED, FAILED).
		Status string `json:"status"`
		// DefaultDatasetID is the ID of the dataset where results are stored.
		DefaultDatasetID string `json:"defaultDatasetId"`
		// Add other potentially useful fields like startedAt, finishedAt, etc.
	} `json:"data"`
	// Error contains details if the API itself returned an error before the run started.
	Error *struct { // Optional field for API-level errors
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// apifyContentCrawlerResponseItem represents a single item from the website-content-crawler dataset.
// Based on the actor description, it focuses on cleaned text/markdown and metadata.
// Adjust fields based on actual actor output if necessary.
type apifyContentCrawlerResponseItem struct {
	// URL is the scraped URL.
	URL string `json:"url"`
	// Text contains the cleaned textual content (or Markdown if configured).
	Text string `json:"text"`
	// Title might be present in metadata, or we extract it if needed.
	// Assuming it might be nested or needs separate extraction if not top-level.
	Metadata *struct {
		Title string `json:"title"`
		// Add other relevant metadata fields here, e.g., author, date
	} `json:"metadata,omitempty"`
	// HTML might be available if configured, but primary focus is text/markdown.
	HTML string `json:"html,omitempty"`

}

// ExtractContent uses the Apify API to run the website-content-crawler actor synchronously for the given URL.
// It returns the first result item from the actor's dataset.
func (a *ApifyExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	// Try the /run-sync-get-dataset-items endpoint again, but using the user~actor format identifier
	// and Authorization header.
	actorIdentifier := "apify~website-content-crawler"
	apiURL := fmt.Sprintf("https://api.apify.com/v2/acts/%s/run-sync-get-dataset-items", actorIdentifier)

	// Prepare the actor input (body of the POST request)
	input := apifyRunInput{
		StartURLs: []struct {
			URL string `json:"url"`
		}{{URL: url}},
	}
	inputBytes, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Apify input: %w", err)
	}

	// Make the POST request
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(inputBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create Apify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+a.apiKey) // Use auth header

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute Apify request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read Apify response body: %w", err)
	}

	// This endpoint can return 200 OK or 201 Created on success
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("Apify API request failed with status %s: %s", resp.Status, string(bodyBytes))
	}

	// The response body is the dataset items as a JSON array.
	var results []apifyContentCrawlerResponseItem
	if err := json.Unmarshal(bodyBytes, &results); err != nil {
		// Log the body for debugging if unmarshalling fails
		fmt.Printf("DEBUG: Failed to unmarshal Apify response as array: %v\nBody: %s\n", err, string(bodyBytes))
		return nil, fmt.Errorf("failed to decode Apify dataset items response: %w. Body: %s", err, string(bodyBytes))
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("Apify actor '%s' returned no results for URL: %s. Response: %s", actorIdentifier, url, string(bodyBytes))
	}

	// Use the first result item
	firstResult := results[0]

	// Attempt to parse date (this actor might provide better metadata)
	var publishedDate *time.Time
	// TODO: Add robust date parsing if the actor provides it in metadata.

	// Extract title from metadata if available
	title := "" // Default to empty title
	if firstResult.Metadata != nil {
		title = firstResult.Metadata.Title
	}

	extracted := &ExtractedContent{
		URL:           firstResult.URL,
		Title:         title,
		Content:       firstResult.Text,
		RawHTML:       firstResult.HTML,
		DatePublished: publishedDate,
	}

	return extracted, nil
} 