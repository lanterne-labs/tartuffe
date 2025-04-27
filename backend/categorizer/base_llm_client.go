package categorizer

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/rs/zerolog/log"
)

// baseLLMClient provides common HTTP request logic for LLM API clients.
type baseLLMClient struct {
	httpClient *http.Client
}

// newBaseLLMClient creates a new base client with a default timeout.
func newBaseLLMClient() *baseLLMClient {
	return &baseLLMClient{
		httpClient: &http.Client{Timeout: 60 * time.Second}, // Default timeout
	}
}

// doRequest performs the HTTP POST request to the LLM API.
// It handles setting headers, sending the payload, and basic status code checking.
// Returns the response body bytes and an error.
func (c *baseLLMClient) doRequest(apiURL, apiKey string, payloadBytes []byte) ([]byte, error) {
	req, err := http.NewRequest("POST", apiURL, bytes.NewBuffer(payloadBytes))
	if err != nil {
		return nil, fmt.Errorf("failed to create LLM API request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	log.Debug().Str("url", apiURL).Msg("Sending request to LLM API")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute LLM API request: %w", err)
	}
	defer resp.Body.Close()

	bodyBytes, readErr := io.ReadAll(resp.Body) // Read body once for potential error logging
	if readErr != nil {
		log.Error().Err(readErr).Str("url", apiURL).Int("status_code", resp.StatusCode).Msg("Failed to read LLM API response body")
		// Continue to check status code, body might be useless anyway
	}

	if resp.StatusCode >= 300 { // Check for non-2xx status codes
		bodyStr := string(bodyBytes)
		maxLen := 250 // Max length for logged body snippet
		if len(bodyStr) > maxLen {
			bodyStr = bodyStr[:maxLen] + "... (truncated)"
		}
		log.Error().Int("status_code", resp.StatusCode).Str("url", apiURL).Str("response_body_snippet", bodyStr).Msg("LLM API request failed")
		// Return bodyBytes along with the error, as it might contain a structured error message
		return bodyBytes, fmt.Errorf("LLM API request to %s failed with status: %s", apiURL, resp.Status)
	}

	log.Debug().Str("url", apiURL).Int("status_code", resp.StatusCode).Msg("Received successful response from LLM API")
	return bodyBytes, nil // Success
} 