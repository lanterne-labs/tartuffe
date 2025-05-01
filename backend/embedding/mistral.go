package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"time"

	"github.com/rs/zerolog/log"
)

const (
	mistralEmbedAPIURLDefault = "https://api.mistral.ai/v1/embeddings"
	mistralEmbedModelDefault  = "mistral-embed"
	maxRetries                = 3               // Maximum number of retries for rate limits
	initialBackoff            = 1 * time.Second // Initial wait time for backoff
	backoffMultiplier         = 2.0             // Factor to multiply backoff by each retry
	maxJitter                 = 0.2             // Max jitter factor (e.g., 0.2 = +/- 20%)
)

// MistralEmbeddingGenerator implements EmbeddingGenerator using the Mistral API.
type MistralEmbeddingGenerator struct {
	apiKey     string
	apiURL     string
	model      string
	httpClient *http.Client
}

// NewMistralEmbeddingGeneratorFromEnv creates a MistralEmbeddingGenerator using settings from env.
func NewMistralEmbeddingGeneratorFromEnv() (*MistralEmbeddingGenerator, error) {
	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("MISTRAL_API_KEY environment variable not set")
	}
	return &MistralEmbeddingGenerator{
		apiKey:     apiKey,
		apiURL:     mistralEmbedAPIURLDefault,
		model:      mistralEmbedModelDefault,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// Structs for Mistral Embedding API
type mistralEmbeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"` // Changed to slice of strings
	// EncodingFormat string `json:"encoding_format,omitempty"` // e.g., "float"
}

type mistralEmbeddingData struct {
	Object    string    `json:"object"` // e.g., "embedding"
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

type mistralEmbeddingUsage struct {
	PromptTokens int `json:"prompt_tokens"`
	TotalTokens  int `json:"total_tokens"`
}

type mistralEmbeddingResponse struct {
	Object string                 `json:"object"` // e.g., "list"
	Data   []mistralEmbeddingData `json:"data"`
	Model  string                 `json:"model"`
	Usage  mistralEmbeddingUsage  `json:"usage"`
	Error  *struct {              // For API-level errors
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code,omitempty"`
	} `json:"error,omitempty"`
}

// GenerateEmbeddings implements the EmbeddingGenerator interface for Mistral, supporting batching.
func (m *MistralEmbeddingGenerator) GenerateEmbeddings(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil // Return empty slice if no input
	}

	reqPayload := mistralEmbeddingRequest{
		Model: m.model,
		Input: texts,
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal Mistral embedding request payload: %w", err)
	}

	var resp *http.Response
	var attempt int
	currentBackoff := initialBackoff

	for attempt = 0; attempt <= maxRetries; attempt++ {
		// Create a new request for each attempt (required as body is consumed)
		req, err := http.NewRequestWithContext(ctx, "POST", m.apiURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			// This error is unlikely to be recoverable by retrying, so fail fast.
			return nil, fmt.Errorf("failed to create Mistral embedding request (attempt %d): %w", attempt+1, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+m.apiKey)

		log.Debug().Str("url", m.apiURL).Int("batch_size", len(texts)).Int("attempt", attempt+1).Msg("Sending request to Mistral Embedding API")

		resp, err = m.httpClient.Do(req)
		if err != nil {
			// Network errors might be transient, consider if retry needed here, but for now, fail.
			// If context is cancelled, stop immediately.
			if ctx.Err() != nil {
				log.Warn().Err(ctx.Err()).Msg("Context cancelled during Mistral API request")
				return nil, fmt.Errorf("context cancelled during Mistral request: %w", ctx.Err())
			}
			log.Error().Err(err).Int("attempt", attempt+1).Msg("Failed to execute Mistral embedding request")
			// Decide if specific network errors warrant a retry, currently failing.
			return nil, fmt.Errorf("failed to execute Mistral embedding request (attempt %d): %w", attempt+1, err)
		}

		// Check for rate limit error (HTTP 429)
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			resp.Body.Close() // Close body before waiting

			// Calculate jitter: +/- maxJitter percentage of current backoff
			jitter := time.Duration((rand.Float64()*2 - 1) * maxJitter * float64(currentBackoff))
			waitTime := currentBackoff + jitter
			if waitTime < 0 {
				waitTime = 0 // Ensure wait time is not negative
			}

			log.Warn().Int("status_code", resp.StatusCode).
				Int("attempt", attempt+1).
				Int("max_retries", maxRetries).
				Dur("wait_time", waitTime).
				Msg("Rate limit hit. Retrying after backoff.")

			select {
			case <-time.After(waitTime):
				currentBackoff = time.Duration(float64(currentBackoff) * backoffMultiplier)
				continue // Retry the loop
			case <-ctx.Done():
				log.Warn().Err(ctx.Err()).Msg("Context cancelled during backoff wait")
				return nil, fmt.Errorf("context cancelled during backoff wait: %w", ctx.Err())
			}
		}

		// If not a rate limit error or retries exhausted, break the loop to process the response
		break
	}

	// Ensure response body is closed if loop finished
	defer resp.Body.Close()

	bodyBytes, readErr := io.ReadAll(resp.Body) // Read body once for potential error logging
	if readErr != nil {
		log.Error().Err(readErr).Str("url", m.apiURL).Int("status_code", resp.StatusCode).Msg("Failed to read Mistral embedding response body after retries")
		// Even if reading fails, try to process the status code
	}

	// Check final status code after potential retries
	if resp.StatusCode != http.StatusOK {
		bodyStr := string(bodyBytes)
		maxLen := 250
		if len(bodyStr) > maxLen {
			bodyStr = bodyStr[:maxLen] + "... (truncated)"
		}
		// Try to parse structured error first
		var mistralResp mistralEmbeddingResponse
		if parseErr := json.Unmarshal(bodyBytes, &mistralResp); parseErr == nil && mistralResp.Error != nil {
			log.Error().Int("status_code", resp.StatusCode).Str("url", m.apiURL).Str("api_error_code", mistralResp.Error.Code).Str("api_error_type", mistralResp.Error.Type).Str("api_error_message", mistralResp.Error.Message).Msg("Mistral embedding API returned error after retries")
			return nil, fmt.Errorf("Mistral embedding API returned error after retries: [%s] %s - %s (HTTP status: %s)", mistralResp.Error.Code, mistralResp.Error.Type, mistralResp.Error.Message, resp.Status)
		}
		// Log generic error if specific one couldn't be parsed
		log.Error().Int("status_code", resp.StatusCode).Str("url", m.apiURL).Str("response_body_snippet", bodyStr).Int("attempts_made", attempt).Msg("Mistral embedding API request failed after retries")
		return nil, fmt.Errorf("Mistral embedding API request failed with status %s after %d attempts", resp.Status, attempt)
	}

	// Parse successful response
	var mistralResp mistralEmbeddingResponse
	if err := json.Unmarshal(bodyBytes, &mistralResp); err != nil {
		bodyStr := string(bodyBytes)
		maxLen := 250
		if len(bodyStr) > maxLen {
			bodyStr = bodyStr[:maxLen] + "... (truncated)"
		}
		log.Error().Err(err).Str("url", m.apiURL).Str("response_body_snippet", bodyStr).Msg("Failed to decode Mistral embedding response JSON")
		return nil, fmt.Errorf("failed to decode Mistral embedding response JSON: %w", err)
	}

	// Check for application-level errors (should have been caught by status code check, but belt-and-suspenders)
	if mistralResp.Error != nil {
		return nil, fmt.Errorf("Mistral embedding API returned error: [%s] %s - %s", mistralResp.Error.Code, mistralResp.Error.Type, mistralResp.Error.Message)
	}

	// Check if the number of returned embeddings matches the input
	if len(mistralResp.Data) != len(texts) {
		return nil, fmt.Errorf("Mistral embedding API returned %d embeddings, but expected %d", len(mistralResp.Data), len(texts))
	}

	// IMPORTANT: Mistral API guarantees the order of embeddings matches the input order.
	// We create the result slice based on this assumption.
	results := make([][]float32, len(texts))
	for _, data := range mistralResp.Data {
		if data.Index >= 0 && data.Index < len(results) {
			results[data.Index] = data.Embedding
		} else {
			// This case should ideally not happen if API guarantees order and index correctness
			log.Warn().Int("invalid_index", data.Index).Int("expected_max_index", len(results)-1).Msg("Received embedding with out-of-bounds index from Mistral API")
		}
	}

	// Final check: ensure no nil entries in the result (e.g., due to bad index)
	for i, emb := range results {
		if emb == nil {
			return nil, fmt.Errorf("missing embedding for input text at index %d", i)
		}
	}

	log.Debug().Int("input_count", len(texts)).Int("output_count", len(results)).Msg("Generated embeddings batch")
	return results, nil
}
