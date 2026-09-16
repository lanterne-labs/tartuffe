package extractor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	apifyActorIDDefault  = "aYG0l9s7dbB7j3gbS" // ID for the "LLM Web Scraper" actor
	apifyBaseURLDefault  = "https://api.apify.com/v2"
	apifyRunSyncEndpoint = "/acts/%s/run-sync-get-dataset-items?token=%s"
)

// ApifyExtractor implements ContentExtractor using the Apify API.
type ApifyExtractor struct {
	apiKey     string
	actorID    string
	baseURL    string
	httpClient *http.Client
}

// NewApifyExtractorFromEnv creates an ApifyExtractor using the API key from env.
func NewApifyExtractorFromEnv() (*ApifyExtractor, error) {
	apiKey := os.Getenv("APIFY_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("APIFY_API_KEY environment variable not set")
	}
	return &ApifyExtractor{
		apiKey:     apiKey,
		actorID:    apifyActorIDDefault,
		baseURL:    apifyBaseURLDefault,
		httpClient: &http.Client{Timeout: 180 * time.Second}, // Long timeout for potentially slow runs
	}, nil
}

// apifyRequest defines the input payload for the Apify actor.
type apifyRequest struct {
	StartURLs         []map[string]string `json:"startUrls"`
	MaxPagesPerDomain int                 `json:"maxPagesPerDomain"`
	// Add other relevant actor input fields if needed
}

// apifyResponseItem mirrors the expected output item structure from the LLM Web Scraper actor.
// We only extract the fields relevant to ExtractedContent.
type apifyResponseItem struct {
	URL     string `json:"url"`
	Title   string `json:"metadata_title"`
	Content string `json:"markdown"` // Using markdown field for cleaner text
	// DatePublished string `json:"metadata_datePublished"` // Need to parse this
}

// ExtractContent implements the ContentExtractor interface for Apify.
func (a *ApifyExtractor) ExtractContent(targetURL string) (*ExtractedContent, error) {
	apiEndpoint := fmt.Sprintf(a.baseURL+apifyRunSyncEndpoint, a.actorID, a.apiKey)

	payload := apifyRequest{
		StartURLs:         []map[string]string{{"url": targetURL}},
		MaxPagesPerDomain: 1, // Only process the starting URL
	}

	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Apify request payload: %w", err)
	}

	log.Debug().Str("url", targetURL).Msg("Sending request to Apify LLM Web Scraper actor")
	req, err := http.NewRequest("POST", apiEndpoint, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create Apify request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute Apify request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, readErr := io.ReadAll(resp.Body) // Read body once
	if readErr != nil {
		log.Error().Err(readErr).Str("url", targetURL).Int("status_code", resp.StatusCode).Msg("Failed to read Apify response body")
		// Fall through to check status code, body might be partial
	}

	// Accept 200 OK or 201 Created as success for this endpoint
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		// Log the error body (truncated) if possible
		bodyStr := string(bodyBytes)
		maxLen := 250 // Max length for logged body snippet
		if len(bodyStr) > maxLen {
			bodyStr = bodyStr[:maxLen] + "... (truncated)"
		}
		log.Error().Int("status_code", resp.StatusCode).Str("url", targetURL).Str("response_body_snippet", bodyStr).Msg("Apify API request failed")
		return nil, fmt.Errorf("Apify API request failed with status: %s", resp.Status) // Don't include full body in error message itself
	}

	// Response is expected to be a JSON array of items
	var responseItems []apifyResponseItem
	if err := json.Unmarshal(bodyBytes, &responseItems); err != nil {
		bodyStr := string(bodyBytes)
		maxLen := 250
		if len(bodyStr) > maxLen {
			bodyStr = bodyStr[:maxLen] + "... (truncated)"
		}
		log.Error().Err(err).Str("url", targetURL).Str("response_body_snippet", bodyStr).Msg("Failed to decode Apify response JSON")
		return nil, fmt.Errorf("failed to decode Apify response JSON: %w", err)
	}

	if len(responseItems) == 0 {
		log.Warn().Str("url", targetURL).Msg("Apify returned no items in the dataset")
		return nil, fmt.Errorf("Apify returned no items for URL: %s", targetURL)
	}

	// Use the first item as the result
	firstItem := responseItems[0]

	// Basic validation
	if firstItem.URL == "" || firstItem.Content == "" {
		log.Warn().Str("target_url", targetURL).Msg("Apify response item missing URL or Content")
		return nil, fmt.Errorf("Apify response item for %s missing URL or Content", targetURL)
	}

	// TODO: Parse DatePublished if needed and available

	log.Debug().Str("url", firstItem.URL).Msg("Successfully extracted content via Apify")
	return &ExtractedContent{
		URL:     firstItem.URL,
		Title:   firstItem.Title,
		Content: firstItem.Content,
		// DatePublished: // Add parsed date here if implemented
	}, nil
}
