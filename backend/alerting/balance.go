// Package alerting provides LLM credit/balance monitoring and Telegram notifications.
package alerting

import (
	"context"
	"time"
)

// BalanceInfo holds the result of a provider balance check.
type BalanceInfo struct {
	Provider   string
	Balance    float64 // remaining USD
	Limit      float64 // total limit (0 = unknown)
	IsFreeTier bool
	CheckedAt  time.Time
	Error      string
}

// BalanceChecker retrieves the current credit balance from an LLM provider.
type BalanceChecker interface {
	ProviderName() string
	CheckBalance(ctx context.Context) (*BalanceInfo, error)
}

// Notifier sends alert messages (e.g., via Telegram).
type Notifier interface {
	Send(ctx context.Context, message string) error
}
