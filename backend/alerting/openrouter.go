package alerting

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// openRouterChecker checks credit balance via the OpenRouter /auth/key endpoint.
type openRouterChecker struct {
	apiURL     string // base URL, e.g. "https://openrouter.ai/api/v1"
	apiKey     string
	httpClient *http.Client
}

// newOpenRouterCheckerFromEnv creates an openRouterChecker if OPEN_AI_API contains "openrouter.ai".
// Returns nil otherwise.
func newOpenRouterCheckerFromEnv() *openRouterChecker {
	apiURL := os.Getenv("OPEN_AI_API")
	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiURL == "" || apiKey == "" {
		return nil
	}
	if !strings.Contains(apiURL, "openrouter.ai") {
		return nil
	}
	return &openRouterChecker{
		apiURL:     apiURL,
		apiKey:     apiKey,
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

func (c *openRouterChecker) ProviderName() string { return "openrouter" }

// openRouterKeyResponse models the /auth/key response.
type openRouterKeyResponse struct {
	Data struct {
		Usage      float64 `json:"usage"`
		Limit      float64 `json:"limit"`
		IsFreeTier bool    `json:"is_free_tier"`
	} `json:"data"`
}

// CheckBalance calls GET /auth/key and computes remaining balance.
func (c *openRouterChecker) CheckBalance(ctx context.Context) (*BalanceInfo, error) {
	url := strings.TrimSuffix(c.apiURL, "/") + "/auth/key"

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openrouter auth/key request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openrouter auth/key returned status %d: %s", resp.StatusCode, string(body))
	}

	var parsed openRouterKeyResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	balance := parsed.Data.Limit - parsed.Data.Usage
	if parsed.Data.Limit == 0 {
		// No limit set (pay-as-you-go or free tier) — balance is unknown/unlimited
		balance = 0
	}

	return &BalanceInfo{
		Provider:   "openrouter",
		Balance:    balance,
		Limit:      parsed.Data.Limit,
		IsFreeTier: parsed.Data.IsFreeTier,
		CheckedAt:  time.Now(),
	}, nil
}
