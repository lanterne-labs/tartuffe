package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/cenkalti/backoff/v4"
	"github.com/rs/zerolog/log"
)

// claudeHTTP sends HTTP requests to the Claude API with the key of the
// account. The batch client and the sync endpoint (ClaudeAPIEndpoint) share
// it.
type claudeHTTP struct {
	httpClient *http.Client
	baseURL    string
	key        string
}

// newClaudeHTTPFromEnv reads ANTHROPIC_API_KEY. timeout is the limit of one
// HTTP request.
func newClaudeHTTPFromEnv(timeout time.Duration) (claudeHTTP, error) {
	key := strings.TrimSpace(os.Getenv("ANTHROPIC_API_KEY"))
	if key == "" {
		return claudeHTTP{}, errors.New("ANTHROPIC_API_KEY environment variable not set")
	}
	return claudeHTTP{
		httpClient: &http.Client{Timeout: timeout},
		baseURL:    claudeBaseURL,
		key:        key,
	}, nil
}

// ClaudeBatchClient talks to the Message Batches API
// (https://platform.claude.com/docs/en/build-with-claude/batch-processing):
// create a batch, read its status, read its results. A batch costs half the
// price of the same sync calls and ends within 24 h.
type ClaudeBatchClient struct {
	claudeHTTP
}

// NewClaudeBatchClientFromEnv reads ANTHROPIC_API_KEY.
func NewClaudeBatchClientFromEnv() (*ClaudeBatchClient, error) {
	h, err := newClaudeHTTPFromEnv(5 * time.Minute)
	if err != nil {
		return nil, err
	}
	return &ClaudeBatchClient{claudeHTTP: h}, nil
}

// ClaudeBatchItem is one request of a batch. CustomID must match
// ^[a-zA-Z0-9_-]{1,64}$ and be unique in the batch.
type ClaudeBatchItem struct {
	CustomID string        `json:"custom_id"`
	Params   ClaudeRequest `json:"params"`
}

// ClaudeBatch is the batch object. ProcessingStatus is "in_progress",
// "canceling" or "ended"; ResultsURL is set when it is "ended".
type ClaudeBatch struct {
	ID               string `json:"id"`
	ProcessingStatus string `json:"processing_status"`
	RequestCounts    struct {
		Processing int `json:"processing"`
		Succeeded  int `json:"succeeded"`
		Errored    int `json:"errored"`
		Canceled   int `json:"canceled"`
		Expired    int `json:"expired"`
	} `json:"request_counts"`
	ResultsURL string    `json:"results_url"`
	CreatedAt  time.Time `json:"created_at"`
}

// ClaudeBatchResult is one line of the results. Type is "succeeded" (Message
// is set), "errored" (Error is set), "canceled" or "expired". Only a
// succeeded request is billed.
type ClaudeBatchResult struct {
	CustomID string
	Type     string
	Message  ClaudeReply
	Error    string
}

// Create submits a batch.
func (c *ClaudeBatchClient) Create(ctx context.Context, items []ClaudeBatchItem) (ClaudeBatch, error) {
	if len(items) == 0 {
		return ClaudeBatch{}, errors.New("claude batch: no request")
	}
	body, err := json.Marshal(map[string]any{"requests": items})
	if err != nil {
		return ClaudeBatch{}, fmt.Errorf("marshal claude batch: %w", err)
	}
	var b ClaudeBatch
	if err := c.doJSON(ctx, http.MethodPost, c.baseURL+"/messages/batches", body, &b); err != nil {
		return ClaudeBatch{}, fmt.Errorf("create claude batch: %w", err)
	}
	return b, nil
}

// Get reads the status of a batch.
func (c *ClaudeBatchClient) Get(ctx context.Context, id string) (ClaudeBatch, error) {
	var b ClaudeBatch
	if err := c.doJSON(ctx, http.MethodGet, c.baseURL+"/messages/batches/"+id, nil, &b); err != nil {
		return ClaudeBatch{}, fmt.Errorf("get claude batch %s: %w", id, err)
	}
	return b, nil
}

// Results reads the results of an ended batch. The order of the lines is not
// the order of the requests: join them by CustomID.
func (c *ClaudeBatchClient) Results(ctx context.Context, b ClaudeBatch) ([]ClaudeBatchResult, error) {
	if b.ProcessingStatus != "ended" || b.ResultsURL == "" {
		return nil, fmt.Errorf("claude batch %s: no results yet (status %q)", b.ID, b.ProcessingStatus)
	}
	body, err := c.do(ctx, http.MethodGet, b.ResultsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("claude batch %s results: %w", b.ID, err)
	}
	return parseClaudeBatchResults(body)
}

// parseClaudeBatchResults decodes the JSONL body of the results.
func parseClaudeBatchResults(body []byte) ([]ClaudeBatchResult, error) {
	var out []ClaudeBatchResult
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for n := 1; sc.Scan(); n++ {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var raw struct {
			CustomID string `json:"custom_id"`
			Result   struct {
				Type    string          `json:"type"`
				Message json.RawMessage `json:"message"`
				Error   json.RawMessage `json:"error"`
			} `json:"result"`
		}
		if err := json.Unmarshal(line, &raw); err != nil {
			return nil, fmt.Errorf("claude batch results line %d: %w", n, err)
		}
		r := ClaudeBatchResult{CustomID: raw.CustomID, Type: raw.Result.Type}
		switch r.Type {
		case "succeeded":
			msg, err := decodeClaudeReply(raw.Result.Message)
			if err != nil {
				return nil, fmt.Errorf("claude batch results line %d: %w", n, err)
			}
			r.Message = msg
		case "errored":
			r.Error = string(raw.Result.Error)
		case "canceled", "expired":
		default:
			return nil, fmt.Errorf("claude batch results line %d: unknown result type %q", n, r.Type)
		}
		out = append(out, r)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read claude batch results: %w", err)
	}
	return out, nil
}

func (c *ClaudeBatchClient) doJSON(ctx context.Context, method, url string, body []byte, out any) error {
	resp, err := c.do(ctx, method, url, body)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(resp, out); err != nil {
		return fmt.Errorf("decode response: %w (raw: %.500s)", err, resp)
	}
	return nil
}

// errClaudeCredit marks the error of an exhausted account: a 402, a 429 for
// the spend limit, or the 400 the API sends when the prepaid credit is gone.
var errClaudeCredit = errors.New("claude account: no credit or spend limit reached")

// isClaudeCreditBody reports whether an error body names an exhausted
// account. On 2026-11-09 the prepaid credit ran out and the API answered
// 400 invalid_request_error "Your credit balance is too low to access the
// Anthropic API", not 402: the worker took it for an error of each document
// and marked 18 000 documents failed in three hours, without an alert.
func isClaudeCreditBody(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "credit balance") || strings.Contains(lower, "billing_error")
}

// do sends one HTTP request with retry. It retries a transport error, a 5xx
// (529 is "overloaded") and a 429, which waits the retry-after header first.
// Every other 4xx is a permanent *ClientError. A 402, a 429 for the spend
// limit and a 400 for the credit balance also call OnCreditError and wrap
// errClaudeCredit: no retry helps until the account changes.
func (c claudeHTTP) do(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var out []byte
	op := func() error {
		var rd io.Reader
		if body != nil {
			rd = bytes.NewReader(body)
		}
		req, err := http.NewRequestWithContext(ctx, method, url, rd)
		if err != nil {
			return backoff.Permanent(fmt.Errorf("build request: %w", err))
		}
		req.Header.Set("x-api-key", c.key)
		req.Header.Set("anthropic-version", claudeAPIVersion)
		req.Header.Set("content-type", "application/json")
		resp, err := c.httpClient.Do(req)
		if err != nil {
			log.Warn().Err(err).Str("url", url).Msg("claude request failed, will retry")
			return err
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			return fmt.Errorf("read response (status %d): %w", resp.StatusCode, err)
		}
		reqID := resp.Header.Get("request-id")
		snippet := string(data)
		if len(snippet) > 500 {
			snippet = snippet[:500] + "... (truncated)"
		}
		switch {
		case resp.StatusCode >= 200 && resp.StatusCode < 300:
			out = data
			return nil
		case resp.StatusCode == http.StatusTooManyRequests:
			if strings.Contains(snippet, "enforced_spend_limit_reached") {
				if OnCreditError != nil {
					OnCreditError("claude", snippet)
				}
				return backoff.Permanent(fmt.Errorf("%w: %w", errClaudeCredit,
					&ClientError{StatusCode: resp.StatusCode, Status: resp.Status, Body: snippet}))
			}
			wait := retryAfter(resp.Header.Get("retry-after"))
			log.Warn().Str("request_id", reqID).Dur("retry_after", wait).Msg("claude rate limit, will retry")
			select {
			case <-ctx.Done():
				return backoff.Permanent(ctx.Err())
			case <-time.After(wait):
			}
			return fmt.Errorf("claude rate limit (status %d)", resp.StatusCode)
		case resp.StatusCode >= 500:
			log.Warn().Str("request_id", reqID).Int("status_code", resp.StatusCode).Str("body", snippet).Msg("claude server error, will retry")
			return fmt.Errorf("claude server error: %s (%s)", resp.Status, snippet)
		default:
			log.Error().Str("request_id", reqID).Int("status_code", resp.StatusCode).Str("body", snippet).Msg("claude request rejected")
			var err error = &ClientError{StatusCode: resp.StatusCode, Status: resp.Status, Body: snippet}
			if resp.StatusCode == http.StatusPaymentRequired || isClaudeCreditBody(snippet) {
				if OnCreditError != nil {
					OnCreditError("claude", snippet)
				}
				err = fmt.Errorf("%w: %w", errClaudeCredit, err)
			}
			return backoff.Permanent(err)
		}
	}
	notify := func(err error, d time.Duration) {
		log.Warn().Err(err).Dur("retry_after", d).Str("url", url).Msg("retrying claude request")
	}
	if err := backoff.RetryNotify(op, createDefaultBackoffPolicy(ctx), notify); err != nil {
		return nil, err
	}
	return out, nil
}

// retryAfter parses the retry-after header (seconds). A missing or bad value
// waits 10 s.
func retryAfter(h string) time.Duration {
	if s, err := strconv.Atoi(strings.TrimSpace(h)); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	return 10 * time.Second
}
