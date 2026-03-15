package alerting

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"digital-mirror/backend/metrics"

	"github.com/rs/zerolog/log"
)

// Alerter orchestrates balance checks and sends alerts when thresholds are breached.
type Alerter struct {
	checkers     []BalanceChecker
	notifier     Notifier
	thresholdUSD float64
	cooldown     time.Duration
	lastAlerted  map[string]time.Time // provider → last alert time
	mu           sync.Mutex
}

// NewAlerter creates a new Alerter. Returns nil if notifier is nil.
func NewAlerter(checkers []BalanceChecker, notifier Notifier, thresholdUSD float64, cooldown time.Duration) *Alerter {
	if notifier == nil || len(checkers) == 0 {
		return nil
	}
	return &Alerter{
		checkers:     checkers,
		notifier:     notifier,
		thresholdUSD: thresholdUSD,
		cooldown:     cooldown,
		lastAlerted:  make(map[string]time.Time),
	}
}

// CheckAndAlert checks all provider balances and alerts if any are below threshold.
func (a *Alerter) CheckAndAlert(ctx context.Context) {
	for _, checker := range a.checkers {
		info, err := checker.CheckBalance(ctx)
		if err != nil {
			log.Warn().Err(err).Str("provider", checker.ProviderName()).Msg("Balance check failed")
			continue
		}

		log.Info().
			Str("provider", info.Provider).
			Float64("balance", info.Balance).
			Float64("limit", info.Limit).
			Bool("free_tier", info.IsFreeTier).
			Msg("Balance check completed")

		// Update Prometheus gauge
		metrics.SetLLMBalance(info.Provider, info.Balance)

		// Skip alerting for free tier or unlimited accounts (limit=0)
		if info.IsFreeTier || info.Limit == 0 {
			continue
		}

		if info.Balance < a.thresholdUSD {
			a.sendAlert(ctx, info.Provider, fmt.Sprintf(
				"<b>Low LLM Balance</b>\nProvider: %s\nBalance: $%.2f\nLimit: $%.2f\nThreshold: $%.2f",
				info.Provider, info.Balance, info.Limit, a.thresholdUSD,
			))
		}
	}
}

// AlertCreditError sends a reactive alert when an LLM call fails due to credit/quota exhaustion.
func (a *Alerter) AlertCreditError(ctx context.Context, provider, errMsg string) {
	// Truncate long error messages
	if len(errMsg) > 300 {
		errMsg = errMsg[:300] + "..."
	}
	a.sendAlert(ctx, provider+"_credit_error", fmt.Sprintf(
		"<b>LLM Credit Error</b>\nProvider: %s\nError: %s",
		provider, errMsg,
	))
}

// sendAlert sends a message via the notifier, respecting per-key cooldown.
func (a *Alerter) sendAlert(ctx context.Context, key, message string) {
	a.mu.Lock()
	last, exists := a.lastAlerted[key]
	if exists && time.Since(last) < a.cooldown {
		a.mu.Unlock()
		log.Debug().Str("key", key).Msg("Alert suppressed (cooldown)")
		return
	}
	a.mu.Unlock()

	if err := a.notifier.Send(ctx, message); err != nil {
		log.Error().Err(err).Str("key", key).Msg("Failed to send alert")
		// Don't update cooldown on failed send — retry next cycle
		return
	}

	a.mu.Lock()
	a.lastAlerted[key] = time.Now()
	a.mu.Unlock()

	log.Info().Str("key", key).Msg("Alert sent")
}

// StartBalancePoller runs periodic balance checks in a loop.
// It runs immediately on start, then every interval.
func StartBalancePoller(ctx context.Context, alerter *Alerter, interval time.Duration) {
	log.Info().Dur("interval", interval).Float64("threshold_usd", alerter.thresholdUSD).Msg("Starting balance alert poller")

	// Run immediately
	alerter.CheckAndAlert(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("Balance poller stopped")
			return
		case <-ticker.C:
			alerter.CheckAndAlert(ctx)
		}
	}
}

// IsCreditError returns true if the HTTP status and response body indicate credit/quota exhaustion.
func IsCreditError(statusCode int, body string) bool {
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
