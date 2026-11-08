package storage

import (
	"context"
	"testing"
	"time"
)

// TestAPISpendLedger: two ledgers (two processes) share the cap of the day;
// a settle replaces a reservation with the real cost; a new day starts at 0.
func TestAPISpendLedger(t *testing.T) {
	ctx := context.Background()
	s, cleanup := setupTestDB(t)
	defer cleanup()
	day := time.Date(2001, 1, 2, 0, 0, 0, 0, time.UTC)
	if _, err := s.Pool().Exec(ctx, `DELETE FROM llm_api_spend WHERE day IN ($1, $2)`, day, day.AddDate(0, 0, 1)); err != nil {
		t.Fatal(err)
	}
	a, b := NewAPISpendLedger(s), NewAPISpendLedger(s)
	if _, ok, err := a.Reserve(ctx, day, 6, 10); err != nil || !ok {
		t.Fatalf("first reservation: ok=%v err=%v", ok, err)
	}
	if spent, ok, err := b.Reserve(ctx, day, 6, 10); err != nil || ok || spent != 6 {
		t.Fatalf("the other process passed the shared cap: spent=%v ok=%v err=%v", spent, ok, err)
	}
	if err := a.Settle(ctx, day, -4); err != nil { // real cost 2
		t.Fatal(err)
	}
	if spent, ok, err := b.Reserve(ctx, day, 6, 10); err != nil || !ok || spent != 8 {
		t.Fatalf("after the settle: spent=%v ok=%v err=%v, want 8 and added", spent, ok, err)
	}
	if spent, ok, err := b.Reserve(ctx, day.AddDate(0, 0, 1), 6, 10); err != nil || !ok || spent != 6 {
		t.Fatalf("a new day: spent=%v ok=%v err=%v", spent, ok, err)
	}
}

// TestAPISpendPhaseAndSpent: the real cost adds up per phase; Spent reads what
// the cap counts, 0 for a day without calls.
func TestAPISpendPhaseAndSpent(t *testing.T) {
	ctx := context.Background()
	s, cleanup := setupTestDB(t)
	defer cleanup()
	day := time.Date(2001, 2, 3, 0, 0, 0, 0, time.UTC)
	for _, table := range []string{"llm_api_spend", "llm_api_spend_phase"} {
		if _, err := s.Pool().Exec(ctx, `DELETE FROM `+table+` WHERE day = $1`, day); err != nil {
			t.Fatal(err)
		}
	}
	l := NewAPISpendLedger(s)
	if spent, err := l.Spent(ctx, day); err != nil || spent != 0 {
		t.Fatalf("a day without calls: spent=%v err=%v", spent, err)
	}
	if _, ok, err := l.Reserve(ctx, day, 3, 10); err != nil || !ok {
		t.Fatalf("reservation: ok=%v err=%v", ok, err)
	}
	if spent, err := l.Spent(ctx, day); err != nil || spent != 3 {
		t.Fatalf("after a reservation: spent=%v err=%v", spent, err)
	}
	for _, c := range []float64{1, 2} {
		if err := l.AddPhase(ctx, day, "maintenance", c); err != nil {
			t.Fatal(err)
		}
	}
	var got float64
	if err := s.Pool().QueryRow(ctx, `SELECT spent_usd FROM llm_api_spend_phase WHERE day = $1 AND phase = 'maintenance'`, day).Scan(&got); err != nil || got != 3 {
		t.Fatalf("the phase: got=%v err=%v", got, err)
	}
}
