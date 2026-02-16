// Package llm provides shared types and utilities for LLM (Large Language Model) API interactions.
// This package is designed to be imported by other packages without creating import cycles.
package llm

import (
	"bytes"
	"context"
	"digital-mirror/backend/metrics"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/rs/zerolog/log"
)

// BaseLLMClient provides common HTTP request logic for LLM API clients.
type BaseLLMClient struct {
	httpClient *http.Client
}

// NewBaseLLMClient creates a new base client with a default timeout.
func NewBaseLLMClient() *BaseLLMClient {
	return &BaseLLMClient{
		httpClient: &http.Client{Timeout: 120 * time.Second},
	}
}

// createDefaultBackoffPolicy creates a reusable exponential backoff policy.
func createDefaultBackoffPolicy(ctx context.Context) backoff.BackOff {
	bo := backoff.NewExponentialBackOff()
	bo.InitialInterval = 1 * time.Second
	bo.Multiplier = 2.0
	bo.MaxInterval = 30 * time.Second
	bo.MaxElapsedTime = 2 * time.Minute
	bo.Reset()
	return backoff.WithContext(bo, ctx)
}

// DoRequest performs the HTTP POST request to the LLM API with context and retry logic.
// Returns the response body bytes and an error.
func (c *BaseLLMClient) DoRequest(ctx context.Context, apiURL, apiKey string, payloadBytes []byte) ([]byte, error) {
	var respBodyBytes []byte
	var httpStatus int
	start := time.Now()

	operation := func() error {
		req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			return backoff.Permanent(fmt.Errorf("failed to create LLM API request: %w", err))
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")

		resp, err := c.httpClient.Do(req)
		if err != nil {
			metrics.IncrementLLMRetry("chat", "connection_error")
			log.Warn().Err(err).Str("url", apiURL).Msg("HTTP request failed during LLM call attempt, will retry")
			return fmt.Errorf("failed to execute LLM API request to %s: %w", apiURL, err)
		}
		defer resp.Body.Close()

		httpStatus = resp.StatusCode

		bodyBytes, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			metrics.IncrementLLMRetry("chat", "read_timeout")
			log.Warn().Err(readErr).Str("url", apiURL).Int("status_code", resp.StatusCode).Msg("Failed to read LLM API response body, will retry")
			return fmt.Errorf("failed to read LLM API response body (status %d): %w", resp.StatusCode, readErr)
		}

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			respBodyBytes = bodyBytes
			return nil
		}

		bodyStr := string(bodyBytes)
		maxLen := 500
		if len(bodyStr) > maxLen {
			bodyStr = bodyStr[:maxLen] + "... (truncated)"
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			metrics.IncrementLLMRetry("chat", "rate_limit")
			log.Warn().Int("status_code", resp.StatusCode).Str("url", apiURL).Msg("LLM API rate limit hit. Will retry.")
			return fmt.Errorf("rate limit exceeded (status %d)", resp.StatusCode)
		}

		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			log.Error().Int("status_code", resp.StatusCode).Str("url", apiURL).Str("response_body_snippet", bodyStr).Msg("LLM API request failed with client error")
			return backoff.Permanent(fmt.Errorf("LLM API client error: %s (%s)", resp.Status, bodyStr))
		}

		if resp.StatusCode >= 500 {
			metrics.IncrementLLMRetry("chat", "server_error")
			log.Warn().Int("status_code", resp.StatusCode).Str("url", apiURL).Str("response_body_snippet", bodyStr).Msg("LLM API server error, will retry")
			return fmt.Errorf("LLM API server error: %s (%s)", resp.Status, bodyStr)
		}

		log.Error().Int("status_code", resp.StatusCode).Str("url", apiURL).Str("response_body_snippet", bodyStr).Msg("LLM API request failed with unexpected status code")
		return backoff.Permanent(fmt.Errorf("unexpected HTTP status %d from LLM API: %s", resp.StatusCode, resp.Status))
	}

	notify := func(err error, d time.Duration) {
		log.Warn().Err(err).Dur("retry_after", d).Str("url", apiURL).Msg("Retrying LLM API request")
	}

	bo := createDefaultBackoffPolicy(ctx)
	err := backoff.RetryNotify(operation, bo, notify)

	duration := time.Since(start)

	if err != nil {
		metrics.ObserveLLMRequest("chat", "", duration, err)
		if httpStatus >= 300 {
			return respBodyBytes, fmt.Errorf("LLM API request failed after retries: %w", err)
		}
		return nil, fmt.Errorf("LLM API request failed after retries: %w", err)
	}

	metrics.ObserveLLMRequest("chat", "", duration, nil)
	log.Debug().Str("url", apiURL).Int("status_code", httpStatus).Dur("duration", duration).Msg("LLM API request completed")
	return respBodyBytes, nil
}

// ChatMessage represents a single message in a chat conversation.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ResponseFormat controls the response format, e.g., JSON mode.
type ResponseFormat struct {
	Type string `json:"type"` // e.g., "json_object"
}

// ProviderConfig controls provider routing for OpenRouter-compatible APIs.
type ProviderConfig struct {
	AllowFallbacks *bool    `json:"allow_fallbacks,omitempty"`
	Ignore         []string `json:"ignore,omitempty"`
	Order          []string `json:"order,omitempty"`
}

// ChatRequest is the OpenAI-compatible chat completion request.
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []ChatMessage   `json:"messages"`
	Temperature    float64         `json:"temperature"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	Provider       *ProviderConfig `json:"provider,omitempty"`
}

// ChatChoice models a single choice in the completion response.
type ChatChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatResponse is the OpenAI-compatible completion response envelope.
type ChatResponse struct {
	Choices []ChatChoice `json:"choices"`
	Error   *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code,omitempty"`
	} `json:"error,omitempty"`
}
