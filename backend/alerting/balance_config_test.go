package alerting

import (
	"testing"
	"time"
)

func TestBalanceConfigFromEnv_Defaults(t *testing.T) {
	t.Setenv("BALANCE_ALERT_THRESHOLD_USD", "")
	t.Setenv("BALANCE_ALERT_COOLDOWN_HOURS", "")
	t.Setenv("BALANCE_CHECK_INTERVAL_MIN", "")

	cfg := BalanceConfigFromEnv()
	if cfg.ThresholdUSD != 2.0 {
		t.Errorf("ThresholdUSD = %v, want 2.0", cfg.ThresholdUSD)
	}
	if cfg.Cooldown != 6*time.Hour {
		t.Errorf("Cooldown = %v, want 6h", cfg.Cooldown)
	}
	if cfg.CheckInterval != 60*time.Minute {
		t.Errorf("CheckInterval = %v, want 60m", cfg.CheckInterval)
	}
}

func TestBalanceConfigFromEnv_Overrides(t *testing.T) {
	t.Setenv("BALANCE_ALERT_THRESHOLD_USD", "5.5")
	t.Setenv("BALANCE_ALERT_COOLDOWN_HOURS", "0.5")
	t.Setenv("BALANCE_CHECK_INTERVAL_MIN", "15")

	cfg := BalanceConfigFromEnv()
	if cfg.ThresholdUSD != 5.5 {
		t.Errorf("ThresholdUSD = %v, want 5.5", cfg.ThresholdUSD)
	}
	if cfg.Cooldown != 30*time.Minute {
		t.Errorf("Cooldown = %v, want 30m", cfg.Cooldown)
	}
	if cfg.CheckInterval != 15*time.Minute {
		t.Errorf("CheckInterval = %v, want 15m", cfg.CheckInterval)
	}
}

func TestBalanceConfigFromEnv_InvalidKeepsDefaults(t *testing.T) {
	t.Setenv("BALANCE_ALERT_THRESHOLD_USD", "-1")
	t.Setenv("BALANCE_ALERT_COOLDOWN_HOURS", "abc")
	t.Setenv("BALANCE_CHECK_INTERVAL_MIN", "0")

	cfg := BalanceConfigFromEnv()
	if cfg.ThresholdUSD != 2.0 || cfg.Cooldown != 6*time.Hour || cfg.CheckInterval != 60*time.Minute {
		t.Errorf("invalid values must keep defaults, got %+v", cfg)
	}
}

func TestNewBalanceAlerterFromEnv_NilWithoutProviders(t *testing.T) {
	t.Setenv("OPEN_AI_API", "")
	t.Setenv("OPEN_AI_KEY", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("TELEGRAM_CHAT_ID", "")

	if got := NewBalanceCheckersFromEnv(); len(got) != 0 {
		t.Errorf("expected no checkers, got %d", len(got))
	}
	if got := NewBalanceAlerterFromEnv(); got != nil {
		t.Errorf("expected nil alerter without checkers/notifier, got %+v", got)
	}
}
