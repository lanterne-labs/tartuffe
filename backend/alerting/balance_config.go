package alerting

import (
	"os"
	"strconv"
	"time"
)

// BalanceConfig is the balance-alert policy shared by the web service's poller
// and the `dm balance check` command.
type BalanceConfig struct {
	// ThresholdUSD is the balance below which a provider is reported LOW / alerted.
	ThresholdUSD float64
	// Cooldown is the minimum gap between two alerts for the same key.
	Cooldown time.Duration
	// CheckInterval is the poller period.
	CheckInterval time.Duration
}

// BalanceConfigFromEnv reads BALANCE_ALERT_THRESHOLD_USD (default 2.0),
// BALANCE_ALERT_COOLDOWN_HOURS (default 6h) and BALANCE_CHECK_INTERVAL_MIN
// (default 60m). An unparsable or non-positive value keeps the default.
func BalanceConfigFromEnv() BalanceConfig {
	cfg := BalanceConfig{
		ThresholdUSD:  2.0,
		Cooldown:      6 * time.Hour,
		CheckInterval: 60 * time.Minute,
	}
	if v := os.Getenv("BALANCE_ALERT_THRESHOLD_USD"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			cfg.ThresholdUSD = f
		}
	}
	if v := os.Getenv("BALANCE_ALERT_COOLDOWN_HOURS"); v != "" {
		if h, err := strconv.ParseFloat(v, 64); err == nil && h > 0 {
			cfg.Cooldown = time.Duration(h * float64(time.Hour))
		}
	}
	if v := os.Getenv("BALANCE_CHECK_INTERVAL_MIN"); v != "" {
		if m, err := strconv.ParseFloat(v, 64); err == nil && m > 0 {
			cfg.CheckInterval = time.Duration(m * float64(time.Minute))
		}
	}
	return cfg
}

// NewBalanceCheckersFromEnv creates every BalanceChecker the environment
// configures (currently OpenRouter only).
func NewBalanceCheckersFromEnv() []BalanceChecker {
	var checkers []BalanceChecker
	if c := newOpenRouterCheckerFromEnv(); c != nil {
		checkers = append(checkers, c)
	}
	return checkers
}

// NewBalanceAlerterFromEnv builds an Alerter from the env-configured checkers,
// Telegram notifier and BalanceConfig. Returns nil when no checker or no
// notifier is available.
func NewBalanceAlerterFromEnv() *Alerter {
	cfg := BalanceConfigFromEnv()
	return newAlerter(NewBalanceCheckersFromEnv(), NewTelegramNotifierFromEnv(), cfg.ThresholdUSD, cfg.Cooldown)
}
