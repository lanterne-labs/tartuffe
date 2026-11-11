package extractor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	firecrawlAPIEndpoint = "https://api.firecrawl.dev/v0/scrape"
	firecrawlAPIKeyEnv   = "FIRECRAWL_API_KEY"
	firecrawlTimeout     = 90 * time.Second // Generous timeout for scraping
)

// FirecrawlExtractor implements the ContentExtractor interface using the Firecrawl API.
type FirecrawlExtractor struct {
	apiKey     string
	httpClient *http.Client
}

// firecrawlRequest defines the structure for the Firecrawl API request body.
type firecrawlRequest struct {
	URL         string                `json:"url"`
	PageOptions *firecrawlPageOptions `json:"pageOptions,omitempty"`
}

// firecrawlPageOptions defines optional scraping parameters.
type firecrawlPageOptions struct {
	OnlyMainContent bool              `json:"onlyMainContent"`
	IncludeHtml     bool              `json:"includeHtml"`       // Request HTML if needed
	Headers         map[string]string `json:"headers,omitempty"` // New field for custom headers
}

// firecrawlResponse defines the structure for the Firecrawl API success response.
type firecrawlResponse struct {
	Success bool `json:"success"`
	Data    struct {
		Content  string `json:"content"`  // This is raw HTML if requested, otherwise main content text/markdown
		Markdown string `json:"markdown"` // Markdown version of the content
		HTML     string `json:"html"`     // Raw HTML, only if requested
		Metadata struct {
			Title         string `json:"title"`
			Description   string `json:"description"`
			PublishedTime string `json:"publishedTime"` // String date, needs parsing
			// URL is the page's URL after the redirects Firecrawl followed;
			// SourceURL the one asked.
			URL       string `json:"url"`
			SourceURL string `json:"sourceURL"`
		} `json:"metadata"`
	} `json:"data"`
	// Message string `json:"message,omitempty"` // Included on failure
}

// NewFirecrawlExtractorFromEnv creates a FirecrawlExtractor if the API key is present.
// Returns an error if the key is missing or initialization fails.
func NewFirecrawlExtractorFromEnv() (*FirecrawlExtractor, error) {
	apiKey := os.Getenv(firecrawlAPIKeyEnv)
	if apiKey == "" {
		log.Debug().Msgf("%s environment variable not set. Skipping Firecrawl extractor.", firecrawlAPIKeyEnv)
		return nil, nil // Indicate key not found, not a fatal error
	}

	// Initialize a standard HTTP client with timeout
	httpClient := &http.Client{
		Timeout: firecrawlTimeout,
	}

	log.Info().Msg("Firecrawl API key found. Initializing Firecrawl extractor.")
	return &FirecrawlExtractor{
		apiKey:     apiKey,
		httpClient: httpClient,
	}, nil
}

// ExtractContent fetches content using the Firecrawl API.
func (e *FirecrawlExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	log.Debug().Str("url", url).Msg("Requesting content extraction via Firecrawl")

	// A common browser User-Agent string
	customUserAgent := "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36"

	requestPayload := firecrawlRequest{
		URL: url,
		PageOptions: &firecrawlPageOptions{
			OnlyMainContent: true,  // Focus on main content
			IncludeHtml:     false, // Don't need raw HTML for now
			Headers: map[string]string{
				"User-Agent": customUserAgent,
			},
		},
	}
	requestBody, err := json.Marshal(requestPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal firecrawl request body for url %s: %w", url, err)
	}

	req, err := http.NewRequest("POST", firecrawlAPIEndpoint, bytes.NewBuffer(requestBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create firecrawl request for url %s: %w", url, err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	startTime := time.Now()
	resp, err := e.httpClient.Do(req)
	duration := time.Since(startTime)
	log.Debug().Str("url", url).Dur("duration_ms", duration).Msg("Firecrawl API call completed")

	if err != nil {
		return nil, fmt.Errorf("firecrawl API request failed for url %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("firecrawl API request failed for url %s with status %d", url, resp.StatusCode)
	}

	var firecrawlResp firecrawlResponse
	if err := json.NewDecoder(resp.Body).Decode(&firecrawlResp); err != nil {
		return nil, fmt.Errorf("failed to decode firecrawl response for url %s: %w", url, err)
	}

	if !firecrawlResp.Success || firecrawlResp.Data.Markdown == "" {
		log.Warn().Str("url", url).Bool("success", firecrawlResp.Success).Msg("Firecrawl API call reported not successful or returned empty markdown content.")
		// Consider returning an error or an empty ExtractedContent based on requirements
		// Returning nil for now, indicating extraction wasn't fruitful.
		return nil, fmt.Errorf("firecrawl extraction not successful or yielded empty content for url %s", url)
	}

	// Parse published date string (handle potential errors)
	var publishedDate *time.Time
	if firecrawlResp.Data.Metadata.PublishedTime != "" {
		// Firecrawl often returns ISO 8601 format
		t, err := time.Parse(time.RFC3339, firecrawlResp.Data.Metadata.PublishedTime)
		if err == nil {
			publishedDate = &t
		} else {
			// Attempt other common formats if needed, or log the parse error
			log.Warn().Str("url", url).Str("date_string", firecrawlResp.Data.Metadata.PublishedTime).Err(err).Msg("Failed to parse publishedTime from Firecrawl metadata")
			// Try parsing with other common formats
			formatsToTry := []string{
				"2006-01-02T15:04:05-0700", // ISO 8601 with numeric timezone (no colon)
				"2006-01-02T15:04:05",      // ISO without Z/offset
				"2006-01-02 15:04:05",
				"Mon, 02 Jan 2006 15:04:05 MST", // RFC1123
				time.RFC1123Z,
			}
			for _, format := range formatsToTry {
				t, err = time.Parse(format, firecrawlResp.Data.Metadata.PublishedTime)
				if err == nil {
					publishedDate = &t
					break
				}
			}
			if publishedDate == nil {
				log.Warn().Str("url", url).Str("date_string", firecrawlResp.Data.Metadata.PublishedTime).Msg("Could not parse publishedTime using common formats.")
			}
		}
	}

	// The page's URL after the redirects, when the API reports it: a Google
	// result link is never the source of a fact.
	finalURL := url
	if m := strings.TrimSpace(firecrawlResp.Data.Metadata.URL); m != "" {
		finalURL = m
	}
	extracted := &ExtractedContent{
		URL:           finalURL,
		Title:         firecrawlResp.Data.Metadata.Title,
		DatePublished: publishedDate,
		Content:       firecrawlResp.Data.Markdown, // Use Markdown as the primary content
		// RawHTML:    firecrawlResp.Data.HTML,    // Populate if requested via IncludeHtml
	}

	log.Debug().Str("url", url).Str("title", extracted.Title).Msg("Successfully extracted content via Firecrawl")
	return extracted, nil
}
