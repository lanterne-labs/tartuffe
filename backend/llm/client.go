// Package llm provides shared types and utilities for LLM (Large Language Model) API interactions.
// This package is designed to be imported by other packages without creating import cycles.
package llm

import (
	"bytes"
	"context"
	"digital-mirror/backend/metrics"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/rs/zerolog/log"
)

// OnCreditError is called when an LLM request fails due to credit/quota exhaustion.
// Set at startup by the alerting system. Safe to leave nil.
var OnCreditError func(provider, errMsg string)

// AppName is the X-Application header value sent on every outbound LLM call —
// surfaced on cachy via gpu-watcher so the user can see who's hammering the
// GPU. If we ever fork this codebase, override here (or expose via env).
const AppName = "digital-mirror"

// BaseLLMClient provides common HTTP request logic for LLM API clients. It calls
// a primary endpoint (the caller-supplied URL/key) and, if configured, fails
// over to a fallback endpoint — the behaviour the LiteLLM proxy used to provide
// in-process. Cloudflare-Access headers are sent to the primary only. Every
// instance also carries a `component` label (e.g. "fact-extractor") sent as the
// X-App-Component header so every cachy-bound call site identifies itself.
type BaseLLMClient struct {
	httpClient     *http.Client
	component      string
	primaryHeaders map[string]string // extra headers for the primary (Cloudflare Access)
	fallbackURL    string            // full chat-completions URL of the fallback (empty = no fallback)
	fallbackKey    string
	fallbackModel  string
}

// NewBaseLLMClient creates a new base client tagged with the given component
// label (sent as the X-App-Component header; pass the step name, e.g.
// "fact-extractor"). Fallback and Cloudflare-Access config is read from the
// environment, all optional: FALLBACK_URL / FALLBACK_KEY / FALLBACK_CHAT_MODEL
// configure the fallback endpoint; CF_ACCESS_CLIENT_ID / CF_ACCESS_CLIENT_SECRET
// are stamped on the primary (home box). Unset ⇒ primary-only, no failover.
func NewBaseLLMClient(component string) *BaseLLMClient {
	if component == "" {
		component = "unknown"
	}
	c := &BaseLLMClient{
		httpClient: &http.Client{Timeout: 300 * time.Second},
		component:  component,
	}
	if id := os.Getenv("CF_ACCESS_CLIENT_ID"); id != "" {
		c.primaryHeaders = map[string]string{
			"CF-Access-Client-Id":     id,
			"CF-Access-Client-Secret": os.Getenv("CF_ACCESS_CLIENT_SECRET"),
		}
	}
	if fb := strings.TrimSpace(os.Getenv("FALLBACK_URL")); fb != "" {
		// The fallback needs its own model — primary and fallback use different
		// providers. Without it, the fallback would be called with the primary's
		// model id (400/404 model-not-found) and silently defeat the failover.
		if model := strings.TrimSpace(os.Getenv("FALLBACK_CHAT_MODEL")); model == "" {
			log.Warn().Msg("FALLBACK_URL is set but FALLBACK_CHAT_MODEL is empty; chat fallback disabled")
		} else {
			c.fallbackURL = buildChatCompletionsURL(fb)
			c.fallbackKey = os.Getenv("FALLBACK_KEY")
			c.fallbackModel = model
		}
	}
	return c
}

// buildChatCompletionsURL composes the chat-completions endpoint from a base URL.
func buildChatCompletionsURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// payloadWithModel returns payloadBytes with its top-level "model" field replaced
// by model (used to swap in the fallback's model on failover). Other fields are
// preserved. If model is empty the payload is returned unchanged.
func payloadWithModel(payloadBytes []byte, model string) ([]byte, error) {
	if model == "" {
		return payloadBytes, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payloadBytes, &m); err != nil {
		return nil, err
	}
	mb, err := json.Marshal(model)
	if err != nil {
		return nil, err
	}
	m["model"] = mb
	return json.Marshal(m)
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

// DoRequest performs the request against the primary endpoint (the caller's
// apiURL/apiKey, with the configured Cloudflare-Access headers and per-endpoint
// retry) and, if that ultimately fails and a fallback is configured, retries the
// fallback endpoint with the fallback model (no CF-Access headers). Returns the
// response body bytes and an error.
func (c *BaseLLMClient) DoRequest(ctx context.Context, apiURL, apiKey string, payloadBytes []byte) ([]byte, error) {
	start := time.Now()

	var body []byte
	var err error
	if c.fallbackURL != "" && PrimaryDown() {
		// Breaker open: the primary is known-unreachable, don't burn the full
		// retry backoff on it — go straight to the fallback below.
		err = errPrimaryBreakerOpen
	} else {
		body, err = c.sendWithRetry(ctx, "primary", apiURL, apiKey, c.primaryHeaders, payloadBytes)
		if err == nil {
			MarkPrimaryUp()
		} else if ctx.Err() == nil && c.fallbackURL != "" && isEndpointOutage(err) {
			MarkPrimaryDown("chat: " + err.Error())
		}
	}
	// Fail over only on a real endpoint failure — not when the caller cancelled.
	if err != nil && ctx.Err() == nil && c.fallbackURL != "" {
		fbPayload, swapErr := payloadWithModel(payloadBytes, c.fallbackModel)
		if swapErr != nil {
			fbPayload = payloadBytes // keep original payload if the model can't be swapped
		}
		log.Warn().Err(err).Str("fallback_url", c.fallbackURL).Msg("primary LLM endpoint failed, trying fallback")
		fbBody, fbErr := c.sendWithRetry(ctx, "openrouter", c.fallbackURL, c.fallbackKey, nil, fbPayload)
		if fbErr == nil {
			body, err = fbBody, nil
		} else {
			err = fmt.Errorf("primary failed (%v); fallback failed: %w", err, fbErr)
		}
	}

	metrics.ObserveLLMRequest("chat", "", time.Since(start), err)
	return body, err
}

// sendWithRetry POSTs to a single endpoint with the exponential-backoff retry,
// credit-error callback, and retry metrics. extraHeaders are set on every
// attempt (used for Cloudflare-Access on the primary). provider labels the
// endpoint for the credit-error callback (e.g. "primary" vs "openrouter").
func (c *BaseLLMClient) sendWithRetry(ctx context.Context, provider, apiURL, apiKey string, extraHeaders map[string]string, payloadBytes []byte) ([]byte, error) {
	var respBodyBytes []byte
	var httpStatus int

	operation := func() error {
		req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewBuffer(payloadBytes))
		if err != nil {
			return backoff.Permanent(fmt.Errorf("failed to create LLM API request: %w", err))
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+apiKey)
		req.Header.Set("Accept", "application/json")
		req.Header.Set("X-Application", AppName)
		req.Header.Set("X-App-Component", c.component)
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}

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
			// Detect credit/quota exhaustion errors and fire callback
			if OnCreditError != nil && isCreditError(resp.StatusCode, bodyStr) {
				OnCreditError(provider, bodyStr)
			}
			log.Error().Int("status_code", resp.StatusCode).Str("url", apiURL).Str("response_body_snippet", bodyStr).Msg("LLM API request failed with client error")
			return backoff.Permanent(&ClientError{StatusCode: resp.StatusCode, Status: resp.Status, Body: bodyStr})
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

	if err != nil {
		if httpStatus >= 300 {
			return respBodyBytes, fmt.Errorf("LLM API request failed after retries: %w", err)
		}
		return nil, fmt.Errorf("LLM API request failed after retries: %w", err)
	}

	log.Debug().Str("url", apiURL).Int("status_code", httpStatus).Msg("LLM API request completed")
	return respBodyBytes, nil
}

// ClientError reports that an endpoint rejected one specific request with a 4xx
// status — an oversized prompt, a bad parameter, exhausted credits. It says
// nothing about the endpoint's health, which is why the circuit breaker ignores
// it: a single over-long prompt used to take the primary offline for ten
// minutes and push a whole pipeline run onto the fallback.
type ClientError struct {
	StatusCode int
	Status     string
	Body       string
}

func (e *ClientError) Error() string {
	return fmt.Sprintf("LLM API client error: %s (%s)", e.Status, e.Body)
}

// isCreditError returns true if the HTTP status and body indicate credit/quota exhaustion.
func isCreditError(statusCode int, body string) bool {
	if statusCode == 402 {
		return true
	}
	lower := strings.ToLower(body)
	if statusCode == 429 && strings.Contains(lower, "resource_exhausted") {
		return true
	}
	return strings.Contains(lower, "insufficient credits") ||
		strings.Contains(lower, "insufficient_quota") ||
		strings.Contains(lower, "payment required")
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
	MaxTokens      int             `json:"max_tokens,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
	Provider       *ProviderConfig `json:"provider,omitempty"`
	// ChatTemplateKwargs forwards arbitrary kwargs to the upstream model's
	// HuggingFace chat template (passed through by llama-server / vLLM via
	// `chat_template_kwargs`). Used today to enable Gemma's thinking mode
	// for judgment-heavy calls: set `{"enable_thinking": true}` and the
	// upstream serves the chain-of-thought in the response's `reasoning`
	// field, keeping `content` clean of the thinking preamble — so our
	// JSON/regex parsers don't need to change.
	//
	// Default empty (no thinking) for fast paths like fact extraction and
	// label generation. Enable explicitly on stance analyze + contradiction
	// detection where the reasoning quality matters.
	ChatTemplateKwargs map[string]any `json:"chat_template_kwargs,omitempty"`
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
