package categorizer

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4" // Import backoff library
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

// createDefaultBackoffPolicy creates a reusable exponential backoff policy.
func createDefaultBackoffPolicy(ctx context.Context) backoff.BackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 1 * time.Second
	bo.Multiplier = 2.0
	bo.MaxInterval = 30 * time.Second
	bo.MaxElapsedTime = 2 * time.Minute // Max time allowed for retries
	bo.Reset()
	// Wrap with context awareness
	return backoff.WithContext(bo, ctx)
}

// doRequest performs the HTTP POST request to the LLM API with context and retry logic using cenkalti/backoff.
// Returns the response body bytes and an error.
func (c *baseLLMClient) doRequest(ctx context.Context, apiURL, apiKey string, payloadBytes []byte) ([]byte, error) {
	var respBodyBytes []byte
	var httpStatus int // Variable to store status code outside the closure if needed

	// Define the operation to be retried
	operation := func() error {
		// Create request (needs to be created inside the operation for retries)
		req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			// Error creating request is likely permanent
			return backoff.Permanent(fmt.Errorf("failed to create LLM API request: %w", err))
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Context cancellation should be treated as permanent by the library via WithContext
			// Network errors might be transient, but currently treated as permanent.
			// Could potentially return non-permanent error here for specific network issues.
			log.Warn().Err(err).Str("url", apiURL).Msg("HTTP request failed during LLM call attempt")
			return backoff.Permanent(fmt.Errorf("failed to execute LLM API request to %s: %w", apiURL, err))
		}
		defer resp.Body.Close()

		httpStatus = resp.StatusCode // Store status code for potential use after retry finishes

		// Read body for potential error messages and successful response
		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			// Error reading body is problematic, likely permanent
			log.Error().Err(readErr).Str("url", apiURL).Int("status_code", resp.StatusCode).Msg("Failed to read LLM API response body")
			return backoff.Permanent(fmt.Errorf("failed to read LLM API response body (status %d): %w", resp.StatusCode, readErr))
		}

		// Store successful response body
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			respBodyBytes = bodyBytes
			return nil // Success! Stop retrying.
		}

		// Handle retryable and permanent errors based on status code
		bodyStr := string(bodyBytes)
		maxLen := 500
		if len(bodyStr) > maxLen {
			bodyStr = bodyStr[:maxLen] + "... (truncated)"
		}

		if resp.StatusCode == http.StatusTooManyRequests { // 429: Rate limit
			log.Warn().Int("status_code", resp.StatusCode).Str("url", apiURL).Msg("LLM API rate limit hit. Will retry.")
			return fmt.Errorf("rate limit exceeded (status %d)", resp.StatusCode) // Signal retry
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 { // 4xx: Client error (permanent)
			log.Error().Int("status_code", resp.StatusCode).Str("url", apiURL).Str("response_body_snippet", bodyStr).Msg("LLM API request failed with client error")
			return backoff.Permanent(fmt.Errorf("LLM API client error: %s (%s)", resp.Status, bodyStr))
		}

		if resp.StatusCode >= 500 { // 5xx: Server error (potentially retryable, but we treat as permanent for now)
			log.Error().Int("status_code", resp.StatusCode).Str("url", apiURL).Str("response_body_snippet", bodyStr).Msg("LLM API request failed with server error")
			// Could return a non-permanent error here if we want to retry 5xx errors.
			return backoff.Permanent(fmt.Errorf("LLM API server error: %s (%s)", resp.Status, bodyStr))
		}

		// Any other status codes (e.g., 3xx redirects which shouldn't happen) are treated as permanent errors
		log.Error().Int("status_code", resp.StatusCode).Str("url", apiURL).Str("response_body_snippet", bodyStr).Msg("LLM API request failed with unexpected status code")
		return backoff.Permanent(fmt.Errorf("unexpected HTTP status %d from LLM API: %s", resp.StatusCode, resp.Status))
	}

	// Define a notification function for logging retries
	notify := func(err error, d time.Duration) {
		log.Warn().Err(err).Dur("retry_after", d).Str("url", apiURL).Msg("Retrying LLM API request")
	}

	// Execute the operation with retry
	bo := createDefaultBackoffPolicy(ctx)
	err := backoff.RetryNotify(operation, bo, notify)

	if err != nil {
		// Error is already logged within the operation closure or notify function
		// The error returned might be the permanent error or the last error after exhausting retries.
		// We might return the bodyBytes obtained during the last failed attempt if status was non-2xx
		if httpStatus >= 300 {
			return respBodyBytes, fmt.Errorf("LLM API request failed after retries: %w", err)
		}
		return nil, fmt.Errorf("LLM API request failed after retries: %w", err)
	}

	// Success case: operation returned nil
	log.Debug().Str("url", apiURL).Int("status_code", httpStatus).Msg("Received successful response from LLM API via backoff retry function")
	return respBodyBytes, nil
}
