package extractor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	webcrawlerAPIKeyEnv       = "WEBCRAWLERAPI_KEY"
	webcrawlerAPIScrapeURL    = "https://api.webcrawlerapi.com/v2/scrape"
	webcrawlerPollInterval    = 1 * time.Second
	webcrawlerMaxPollAttempts = 120 // 120 * 1s = 120 seconds max
	webcrawlerHTTPTimeout     = 30 * time.Second
)

// WebcrawlerAPIExtractor implements the ContentExtractor interface using webcrawlerapi.com.
type WebcrawlerAPIExtractor struct {
	apiKey     string
	httpClient *http.Client
}

// webcrawlerScrapeRequest defines the request body for the scrape endpoint.
type webcrawlerScrapeRequest struct {
	URL        string                   `json:"url"`
	ScrapeType string                   `json:"scrape_type"`
	ItemsLimit int                      `json:"items_limit,omitempty"`
	Items      []webcrawlerRequestItems `json:"items,omitempty"`
}

type webcrawlerRequestItems struct {
	Name     string `json:"name"`
	Selector string `json:"selector,omitempty"`
}

// webcrawlerScrapeResponse defines the response from the scrape endpoint.
type webcrawlerScrapeResponse struct {
	ID             string `json:"id"`
	Status         string `json:"status"`
	Success        bool   `json:"success"`
	Markdown       string `json:"markdown"`
	PageStatusCode int    `json:"page_status_code"`
	PageTitle      string `json:"page_title"`
	Message        string `json:"message,omitempty"`
}

// NewWebcrawlerAPIExtractorFromEnv creates a WebcrawlerAPIExtractor if the API key is present.
// Returns (nil, nil) if the key is not set (allows fallback to other extractors).
func NewWebcrawlerAPIExtractorFromEnv() (*WebcrawlerAPIExtractor, error) {
	apiKey := os.Getenv(webcrawlerAPIKeyEnv)
	if apiKey == "" {
		log.Debug().Msgf("%s environment variable not set. Skipping WebcrawlerAPI extractor.", webcrawlerAPIKeyEnv)
		return nil, nil
	}

	httpClient := &http.Client{
		Timeout: webcrawlerHTTPTimeout,
	}

	log.Info().Msg("WebcrawlerAPI key found. Initializing WebcrawlerAPI extractor.")
	return &WebcrawlerAPIExtractor{
		apiKey:     apiKey,
		httpClient: httpClient,
	}, nil
}

// ExtractContent fetches content using the WebcrawlerAPI.
func (e *WebcrawlerAPIExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	log.Debug().Str("url", url).Msg("Requesting content extraction via WebcrawlerAPI")

	// Submit the scrape job
	jobID, err := e.submitScrapeJob(url)
	if err != nil {
		return nil, fmt.Errorf("failed to submit scrape job for url %s: %w", url, err)
	}

	log.Debug().Str("url", url).Str("job_id", jobID).Msg("WebcrawlerAPI job submitted, polling for completion")

	// Poll for completion
	result, err := e.pollForCompletion(jobID, url)
	if err != nil {
		return nil, fmt.Errorf("failed to poll for scrape completion for url %s: %w", url, err)
	}

	// Validate the result
	if !result.Success {
		msg := result.Message
		if msg == "" {
			msg = fmt.Sprintf("status=%s, page_status_code=%d", result.Status, result.PageStatusCode)
		}
		return nil, fmt.Errorf("webcrawlerapi extraction failed for url %s: %s", url, msg)
	}

	if result.Markdown == "" {
		return nil, fmt.Errorf("webcrawlerapi extraction returned empty markdown for url %s", url)
	}

	extracted := &ExtractedContent{
		URL:           url,
		Title:         result.PageTitle,
		DatePublished: nil, // WebcrawlerAPI doesn't provide publication date
		Content:       result.Markdown,
	}

	log.Debug().Str("url", url).Str("title", extracted.Title).Msg("Successfully extracted content via WebcrawlerAPI")
	return extracted, nil
}

// submitScrapeJob submits a scrape job to the WebcrawlerAPI and returns the job ID.
func (e *WebcrawlerAPIExtractor) submitScrapeJob(url string) (string, error) {
	requestPayload := webcrawlerScrapeRequest{
		URL:        url,
		ScrapeType: "markdown",
	}

	requestBody, err := json.Marshal(requestPayload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequest("POST", webcrawlerAPIScrapeURL+"?async=true", bytes.NewBuffer(requestBody))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("API returned status %d", resp.StatusCode)
	}

	var scrapeResp webcrawlerScrapeResponse
	if err := json.NewDecoder(resp.Body).Decode(&scrapeResp); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}

	if scrapeResp.ID == "" {
		return "", fmt.Errorf("API returned empty job ID")
	}

	return scrapeResp.ID, nil
}

// pollForCompletion polls the WebcrawlerAPI until the job is done or times out.
func (e *WebcrawlerAPIExtractor) pollForCompletion(jobID, url string) (*webcrawlerScrapeResponse, error) {
	pollURL := fmt.Sprintf("%s/%s", webcrawlerAPIScrapeURL, jobID)

	for attempt := 1; attempt <= webcrawlerMaxPollAttempts; attempt++ {
		req, err := http.NewRequest("GET", pollURL, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create poll request: %w", err)
		}

		req.Header.Set("Authorization", "Bearer "+e.apiKey)

		resp, err := e.httpClient.Do(req)
		if err != nil {
			// Transient network error, retry
			log.Warn().Err(err).Str("job_id", jobID).Int("attempt", attempt).Msg("WebcrawlerAPI poll request failed, retrying")
			time.Sleep(webcrawlerPollInterval)
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return nil, fmt.Errorf("job not found: %s", jobID)
		}

		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			log.Warn().Int("status", resp.StatusCode).Str("job_id", jobID).Int("attempt", attempt).Msg("WebcrawlerAPI poll returned non-200, retrying")
			time.Sleep(webcrawlerPollInterval)
			continue
		}

		var scrapeResp webcrawlerScrapeResponse
		if err := json.NewDecoder(resp.Body).Decode(&scrapeResp); err != nil {
			resp.Body.Close()
			return nil, fmt.Errorf("failed to decode poll response: %w", err)
		}
		resp.Body.Close()

		log.Debug().
			Str("job_id", jobID).
			Str("status", scrapeResp.Status).
			Int("attempt", attempt).
			Msg("WebcrawlerAPI poll status")

		// Check if job is complete
		if scrapeResp.Status == "done" {
			return &scrapeResp, nil
		}

		// Check for failure status
		if scrapeResp.Status != "new" && scrapeResp.Status != "pending" && scrapeResp.Status != "in_progress" && scrapeResp.Status != "running" {
			return nil, fmt.Errorf("job failed with status: %s, message: %s", scrapeResp.Status, scrapeResp.Message)
		}

		// Still pending, wait and retry
		time.Sleep(webcrawlerPollInterval)
	}

	return nil, fmt.Errorf("polling timed out after %d attempts for url %s", webcrawlerMaxPollAttempts, url)
}
