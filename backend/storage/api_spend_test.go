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
