package embedding

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"os"
	"strings"
	"time"
	"unicode"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"

	"github.com/rs/zerolog/log"
)

const (
	maxRetries        = 3               // Maximum number of retries for rate limits
	initialBackoff    = 1 * time.Second // Initial wait time for backoff
	backoffMultiplier = 2.0             // Factor to multiply backoff by each retry
	maxJitter         = 0.2             // Max jitter factor (e.g., 0.2 = +/- 20%)
	// targetEmbeddingDim MUST equal the dimension of facts.embedding /
	// stance_documents.cluster_embedding in the schema. If the embedder returns a
	// larger vector (e.g. Qwen3-Embedding-8B at 4096 vs an old 1536-d schema), it
	// is Matryoshka-truncated to this prefix and L2-renormalized — Qwen3 was
	// trained with nested MRL loss so the leading prefix is a valid lower-dim
	// embedding. When you swap the embedding model AND the column dimension via a
	// migration, update this constant in the same change — otherwise pgvector
	// rejects every insert with "expected N dimensions, not M".
	targetEmbeddingDim = 4096
)

// openAIEmbedModelDefault uses text-embedding-3-small (1536 dimensions) by default
const openAIEmbedModelDefault = "text-embedding-3-small"

// buildEmbedAPIURL composes the full embeddings endpoint from the OPEN_AI_API base.
func buildEmbedAPIURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/embeddings") {
		return base
	}
	return base + "/embeddings"
}

// OpenAIEmbeddingGenerator implements EmbeddingGenerator for OpenAI-compatible APIs.
type OpenAIEmbeddingGenerator struct {
	// apiKey authenticates against the provider
	apiKey string
	// apiURL is the embeddings endpoint
	apiURL string
	// model is the embeddings model identifier
	model string
	// httpClient performs HTTP requests
	httpClient *http.Client
}

// NewOpenAIEmbeddingGeneratorFromEnv creates an OpenAIEmbeddingGenerator using OPEN_AI_ env vars.
// Required: OPEN_AI_KEY, OPEN_AI_API (base, e.g., https://api.openai.com/v1)
// Optional: OPEN_AI_EMBED_MODEL (defaults to openAIEmbedModelDefault)
func NewOpenAIEmbeddingGeneratorFromEnv() (*OpenAIEmbeddingGenerator, error) {
	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPEN_AI_KEY environment variable not set")
	}
	base := os.Getenv("OPEN_AI_API")
	if base == "" {
		return nil, fmt.Errorf("OPEN_AI_API environment variable not set")
	}
	model := os.Getenv("OPEN_AI_EMBED_MODEL")
	if strings.TrimSpace(model) == "" {
		model = openAIEmbedModelDefault
	}
	return &OpenAIEmbeddingGenerator{
		apiKey:     apiKey,
		apiURL:     buildEmbedAPIURL(base),
		model:      model,
		httpClient: &http.Client{Timeout: 60 * time.Second},
	}, nil
}

// embedRequest models the OpenAI-compatible embeddings request payload.
type embedRequest struct {
	// Model identifier to use for embeddings
	Model string `json:"model"`
	// Input texts to embed (batch)
	Input []string `json:"input"`
}

// embedData represents a single embedding vector result.
type embedData struct {
	// Object type, e.g., "embedding"
	Object string `json:"object"`
	// Embedding vector for the corresponding input index
	Embedding []float32 `json:"embedding"`
	// Index of the input text this embedding corresponds to
	Index int `json:"index"`
}

// embedUsage summarizes token usage returned by the API, if any.
type embedUsage struct {
	// PromptTokens consumed
	PromptTokens int `json:"prompt_tokens"`
	// TotalTokens consumed
	TotalTokens int `json:"total_tokens"`
}

// embedResponse is the OpenAI-compatible embeddings response envelope.
type embedResponse struct {
	// Object type, e.g., "list"
	Object string `json:"object"`
	// Data contains the embeddings for each input
	Data []embedData `json:"data"`
	// Model used
	Model string `json:"model"`
	// Usage statistics
	Usage embedUsage `json:"usage"`
	// Error information, if present
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Code    string `json:"code,omitempty"`
	} `json:"error,omitempty"`
}

// purposeInstructions maps each Purpose to the instruction prefix prepended
// to every input. Qwen3-Embedding consumes "Instruct: ...\nQuery: ..." and
// produces a representation tuned to the stated task; OpenAI's
// text-embedding-* models ignore the prefix harmlessly, so the same code path
// works during the cutover.
var purposeInstructions = map[Purpose]string{
	PurposeFactStorage:         "Represent this political claim by a public figure so that semantically related or contradicting claims by the same or other figures can be retrieved.",
	PurposeContradictionBundle: "Represent the underlying inconsistency between two political claims (claim A versus claim B and the reason they conflict) so that other contradictions involving the same broken promise, reversal, or topical inconsistency can be retrieved — not merely other claims on the same topic.",
}

// sanitizeForEmbedding removes control characters that carry no semantic meaning
// and could confuse the tokenizer or API. Keeps all printable Unicode (letters,
// digits, punctuation, symbols from all scripts including accented chars, CJK, Arabic, etc.)
// plus whitespace (space, tab, newline, carriage-return).
func sanitizeForEmbedding(text string) string {
	var b strings.Builder
	b.Grow(len(text))
	for _, r := range text {
		if r == '\t' || r == '\n' || r == '\r' || unicode.IsGraphic(r) {
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// matryoshkaTruncate keeps the first targetEmbeddingDim values of v and
// re-L2-normalizes; if v is already <= targetEmbeddingDim it is returned as-is
// (callers downstream will fail loudly if the dim doesn't match the storage
// column, which is the correct behavior).
func matryoshkaTruncate(v []float32) []float32 {
	if len(v) <= targetEmbeddingDim {
		return v
	}
	out := make([]float32, targetEmbeddingDim)
	copy(out, v[:targetEmbeddingDim])
	var sumsq float64
	for _, x := range out {
		sumsq += float64(x) * float64(x)
	}
	if sumsq == 0 {
		return out
	}
	inv := float32(1.0 / math.Sqrt(sumsq))
	for i := range out {
		out[i] *= inv
	}
	return out
}

// GenerateEmbeddings implements the EmbeddingGenerator interface for OpenAI-compatible APIs, supporting batching.
func (m *OpenAIEmbeddingGenerator) GenerateEmbeddings(ctx context.Context, purpose Purpose, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return [][]float32{}, nil // Return empty slice if no input
	}

	instruction, ok := purposeInstructions[purpose]
	if !ok {
		return nil, fmt.Errorf("unknown embedding purpose %q", purpose)
	}

	prefixed := make([]string, len(texts))
	for i, t := range texts {
		prefixed[i] = "Instruct: " + instruction + "\nQuery: " + sanitizeForEmbedding(t)
	}

	// Record batch size for metrics
	metrics.ObserveEmbeddingBatchSize(len(texts))
	timer := metrics.NewLLMTimer("embedding", m.model)

	reqPayload := embedRequest{
		Model: m.model,
		Input: prefixed,
	}

	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		timer.ObserveDuration(err)
		return nil, fmt.Errorf("failed to marshal embeddings request payload: %w", err)
	}

	var resp *http.Response
	var attempt int
	currentBackoff := initialBackoff

	for attempt = 0; attempt <= maxRetries; attempt++ {
		// Create a new request for each attempt (required as body is consumed)
		req, err := http.NewRequestWithContext(ctx, "POST", m.apiURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			// This error is unlikely to be recoverable by retrying, so fail fast.
			return nil, fmt.Errorf("failed to create embeddings request (attempt %d): %w", attempt+1, err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
		req.Header.Set("X-Application", llm.AppName)
		req.Header.Set("X-App-Component", "embedder")

		log.Debug().Str("url", m.apiURL).Int("batch_size", len(texts)).Int("attempt", attempt+1).Msg("Sending request to embeddings API")

		resp, err = m.httpClient.Do(req)
		if err != nil {
			// Network errors might be transient, consider if retry needed here, but for now, fail.
			// If context is cancelled, stop immediately.
			if ctx.Err() != nil {
				log.Warn().Err(ctx.Err()).Msg("Context cancelled during embeddings API request")
				return nil, fmt.Errorf("context cancelled during embeddings request: %w", ctx.Err())
			}
			log.Error().Err(err).Int("attempt", attempt+1).Msg("Failed to execute embeddings request")
			// Decide if specific network errors warrant a retry, currently failing.
			return nil, fmt.Errorf("failed to execute embeddings request (attempt %d): %w", attempt+1, err)
		}

		// Check for rate limit error (HTTP 429)
		if resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries {
			resp.Body.Close() // Close body before waiting

			// Record retry metric
			metrics.IncrementLLMRetry("embedding", "rate_limit")

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
		log.Error().Err(readErr).Str("url", m.apiURL).Int("status_code", resp.StatusCode).Msg("Failed to read embeddings response body after retries")
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
		var embResp embedResponse
		if parseErr := json.Unmarshal(bodyBytes, &embResp); parseErr == nil && embResp.Error != nil {
			log.Error().Int("status_code", resp.StatusCode).Str("url", m.apiURL).Str("api_error_code", embResp.Error.Code).Str("api_error_type", embResp.Error.Type).Str("api_error_message", embResp.Error.Message).Msg("Embeddings API returned error after retries")
			err := fmt.Errorf("Embeddings API returned error after retries: [%s] %s - %s (HTTP status: %s)", embResp.Error.Code, embResp.Error.Type, embResp.Error.Message, resp.Status)
			timer.ObserveDuration(err)
			return nil, err
		}
		// Log generic error if specific one couldn't be parsed
		log.Error().Int("status_code", resp.StatusCode).Str("url", m.apiURL).Str("response_body_snippet", bodyStr).Int("attempts_made", attempt).Msg("Embeddings API request failed after retries")
		err := fmt.Errorf("Embeddings API request failed with status %s after %d attempts", resp.Status, attempt)
		timer.ObserveDuration(err)
		return nil, err
	}

	// Parse successful response
	var embResp embedResponse
	if err := json.Unmarshal(bodyBytes, &embResp); err != nil {
		bodyStr := string(bodyBytes)
		maxLen := 250
		if len(bodyStr) > maxLen {
			bodyStr = bodyStr[:maxLen] + "... (truncated)"
		}
		log.Error().Err(err).Str("url", m.apiURL).Str("response_body_snippet", bodyStr).Msg("Failed to decode embeddings response JSON")
		return nil, fmt.Errorf("failed to decode embeddings response JSON: %w", err)
	}

	// Check for application-level errors (should have been caught by status code check, but belt-and-suspenders)
	if embResp.Error != nil {
		return nil, fmt.Errorf("Embeddings API returned error: [%s] %s - %s", embResp.Error.Code, embResp.Error.Type, embResp.Error.Message)
	}

	// Check if the number of returned embeddings matches the input
	if len(embResp.Data) != len(texts) {
		return nil, fmt.Errorf("Embeddings API returned %d embeddings, but expected %d", len(embResp.Data), len(texts))
	}

	// IMPORTANT: Most OpenAI-compatible APIs guarantee the order of embeddings matches the input order.
	// We create the result slice based on this assumption.
	results := make([][]float32, len(texts))
	for _, data := range embResp.Data {
		if data.Index >= 0 && data.Index < len(results) {
			results[data.Index] = matryoshkaTruncate(data.Embedding)
		} else {
			// This case should ideally not happen if API guarantees order and index correctness
			log.Warn().Int("invalid_index", data.Index).Int("expected_max_index", len(results)-1).Msg("Received embedding with out-of-bounds index from embeddings API")
		}
	}

	// Final check: ensure no nil entries in the result (e.g., due to bad index)
	for i, emb := range results {
		if emb == nil {
			err := fmt.Errorf("missing embedding for input text at index %d", i)
			timer.ObserveDuration(err)
			return nil, err
		}
	}

	timer.ObserveDuration(nil)
	log.Debug().Int("input_count", len(texts)).Int("output_count", len(results)).Msg("Generated embeddings batch")
	return results, nil
}
