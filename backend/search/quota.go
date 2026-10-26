package search

import (
	"errors"
	"net/http"
	"strings"
)

// ErrQuota marks a search the provider refused for its quota or its rate
// (Serper "Not enough credits", Brave's open circuit, SerpAPI 429). The search
// was not made: the caller waits for the quota instead of taking the period
// as searched or failed.
var ErrQuota = errors.New("search quota exhausted")

// quotaStatus reports whether an HTTP status is a provider's quota answer.
func quotaStatus(code int) bool {
	return code == http.StatusTooManyRequests || code == http.StatusPaymentRequired
}

// quotaMessage reports whether a provider's error text names its quota.
func quotaMessage(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "not enough credits") || strings.Contains(m, "run out of searches") ||
		strings.Contains(m, "quota") || strings.Contains(m, "rate limit")
}
