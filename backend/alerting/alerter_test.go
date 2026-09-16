package alerting

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// mockChecker is a test BalanceChecker.
type mockChecker struct {
	name string
	info *BalanceInfo
	err  error
}

func (m *mockChecker) ProviderName() string { return m.name }
func (m *mockChecker) CheckBalance(_ context.Context) (*BalanceInfo, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.info, nil
}

// mockNotifier records sent messages.
type mockNotifier struct {
	mu       sync.Mutex
	messages []string
	err      error
}

func (m *mockNotifier) Send(_ context.Context, message string) error {
	if m.err != nil {
		return m.err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.messages = append(m.messages, message)
	return nil
}

func (m *mockNotifier) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.messages)
}

func TestAlertBelowThreshold(t *testing.T) {
	checker := &mockChecker{
		name: "openrouter",
		info: &BalanceInfo{
			Provider: "openrouter",
			Balance:  1.50,
			Limit:    10.00,
		},
	}
	notifier := &mockNotifier{}
	alerter := newAlerter([]BalanceChecker{checker}, notifier, 2.0, 6*time.Hour)

	alerter.checkAndAlert(context.Background())

	if notifier.count() != 1 {
		t.Fatalf("expected 1 alert, got %d", notifier.count())
	}
}

func TestNoAlertAboveThreshold(t *testing.T) {
	checker := &mockChecker{
		name: "openrouter",
		info: &BalanceInfo{
			Provider: "openrouter",
			Balance:  5.00,
			Limit:    10.00,
		},
	}
	notifier := &mockNotifier{}
	alerter := newAlerter([]BalanceChecker{checker}, notifier, 2.0, 6*time.Hour)

	alerter.checkAndAlert(context.Background())

	if notifier.count() != 0 {
		t.Fatalf("expected 0 alerts, got %d", notifier.count())
	}
}

func TestCooldownSuppressesDuplicate(t *testing.T) {
	checker := &mockChecker{
		name: "openrouter",
		info: &BalanceInfo{
			Provider: "openrouter",
			Balance:  0.50,
			Limit:    10.00,
		},
	}
	notifier := &mockNotifier{}
	alerter := newAlerter([]BalanceChecker{checker}, notifier, 2.0, 6*time.Hour)

	// First alert goes through
	alerter.checkAndAlert(context.Background())
	if notifier.count() != 1 {
		t.Fatalf("expected 1 alert, got %d", notifier.count())
	}

	// Second alert suppressed by cooldown
	alerter.checkAndAlert(context.Background())
	if notifier.count() != 1 {
		t.Fatalf("expected still 1 alert (cooldown), got %d", notifier.count())
	}
}

func TestCooldownExpiresAllowsNewAlert(t *testing.T) {
	checker := &mockChecker{
		name: "openrouter",
		info: &BalanceInfo{
			Provider: "openrouter",
			Balance:  0.50,
			Limit:    10.00,
		},
	}
	notifier := &mockNotifier{}
	alerter := newAlerter([]BalanceChecker{checker}, notifier, 2.0, 50*time.Millisecond)

	alerter.checkAndAlert(context.Background())
	if notifier.count() != 1 {
		t.Fatalf("expected 1 alert, got %d", notifier.count())
	}

	// Wait for cooldown to expire
	time.Sleep(60 * time.Millisecond)

	alerter.checkAndAlert(context.Background())
	if notifier.count() != 2 {
		t.Fatalf("expected 2 alerts (cooldown expired), got %d", notifier.count())
	}
}

func TestAlertCreditErrorDedup(t *testing.T) {
	checker := &mockChecker{
		name: "openrouter",
		info: &BalanceInfo{Provider: "openrouter", Balance: 5.0, Limit: 10.0},
	}
	notifier := &mockNotifier{}
	alerter := newAlerter([]BalanceChecker{checker}, notifier, 2.0, 6*time.Hour)

	alerter.AlertCreditError(context.Background(), "openrouter", "insufficient credits")
	if notifier.count() != 1 {
		t.Fatalf("expected 1 alert, got %d", notifier.count())
	}

	// Duplicate suppressed
	alerter.AlertCreditError(context.Background(), "openrouter", "insufficient credits again")
	if notifier.count() != 1 {
		t.Fatalf("expected still 1 alert, got %d", notifier.count())
	}
}

func TestMultipleProvidersIndependentCooldowns(t *testing.T) {
	checkerA := &mockChecker{
		name: "openrouter",
		info: &BalanceInfo{Provider: "openrouter", Balance: 0.5, Limit: 10.0},
	}
	checkerB := &mockChecker{
		name: "gemini",
		info: &BalanceInfo{Provider: "gemini", Balance: 0.5, Limit: 10.0},
	}
	notifier := &mockNotifier{}
	alerter := newAlerter([]BalanceChecker{checkerA, checkerB}, notifier, 2.0, 6*time.Hour)

	alerter.checkAndAlert(context.Background())
	if notifier.count() != 2 {
		t.Fatalf("expected 2 alerts (one per provider), got %d", notifier.count())
	}

	// Both are now in cooldown
	alerter.checkAndAlert(context.Background())
	if notifier.count() != 2 {
		t.Fatalf("expected still 2 alerts, got %d", notifier.count())
	}
}

func TestNoAlertForFreeTier(t *testing.T) {
	checker := &mockChecker{
		name: "openrouter",
		info: &BalanceInfo{
			Provider:   "openrouter",
			Balance:    0.0,
			Limit:      0.0,
			IsFreeTier: true,
		},
	}
	notifier := &mockNotifier{}
	alerter := newAlerter([]BalanceChecker{checker}, notifier, 2.0, 6*time.Hour)

	alerter.checkAndAlert(context.Background())
	if notifier.count() != 0 {
		t.Fatalf("expected 0 alerts for free tier, got %d", notifier.count())
	}
}

func TestFailedSendDoesNotUpdateCooldown(t *testing.T) {
	checker := &mockChecker{
		name: "openrouter",
		info: &BalanceInfo{Provider: "openrouter", Balance: 0.5, Limit: 10.0},
	}
	notifier := &mockNotifier{err: fmt.Errorf("network error")}
	alerter := newAlerter([]BalanceChecker{checker}, notifier, 2.0, 6*time.Hour)

	// First attempt fails
	alerter.checkAndAlert(context.Background())
	if notifier.count() != 0 {
		t.Fatalf("expected 0 (error swallowed), got %d", notifier.count())
	}

	// Should retry since cooldown was not set
	notifier.err = nil
	alerter.checkAndAlert(context.Background())
	if notifier.count() != 1 {
		t.Fatalf("expected 1 alert after retry, got %d", notifier.count())
	}
}

func TestNewAlerterReturnsNilWithoutNotifier(t *testing.T) {
	checker := &mockChecker{name: "test"}
	if a := newAlerter([]BalanceChecker{checker}, nil, 2.0, time.Hour); a != nil {
		t.Fatal("expected nil alerter with nil notifier")
	}
}

func TestNewAlerterReturnsNilWithoutCheckers(t *testing.T) {
	notifier := &mockNotifier{}
	if a := newAlerter(nil, notifier, 2.0, time.Hour); a != nil {
		t.Fatal("expected nil alerter with no checkers")
	}
}
