package alerting

import (
	"testing"
	"time"
)

func TestEvaluatePipelineHealth(t *testing.T) {
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-20 * time.Hour)
	stale := now.Add(-40 * time.Hour)

	t.Run("healthy: no alerts", func(t *testing.T) {
		msgs := evaluatePipelineHealth(PipelineHealth{LastOKStart: &recent}, now, map[string]time.Time{})
		if len(msgs) != 0 {
			t.Errorf("expected no alerts, got %v", msgs)
		}
	})

	t.Run("silence >30h alerts", func(t *testing.T) {
		msgs := evaluatePipelineHealth(PipelineHealth{LastOKStart: &stale}, now, map[string]time.Time{})
		if len(msgs) != 1 {
			t.Fatalf("expected 1 alert, got %v", msgs)
		}
	})

	t.Run("no run ever alerts", func(t *testing.T) {
		msgs := evaluatePipelineHealth(PipelineHealth{}, now, map[string]time.Time{})
		if len(msgs) != 1 {
			t.Fatalf("expected 1 alert, got %v", msgs)
		}
	})

	t.Run("stuck and failed alert together", func(t *testing.T) {
		msgs := evaluatePipelineHealth(PipelineHealth{LastOKStart: &recent, StuckRunning: 1, RecentFailed: 2}, now, map[string]time.Time{})
		if len(msgs) != 2 {
			t.Fatalf("expected 2 alerts, got %v", msgs)
		}
	})

	t.Run("cooldown suppresses repeats but not new conditions", func(t *testing.T) {
		lastSent := map[string]time.Time{}
		if n := len(evaluatePipelineHealth(PipelineHealth{LastOKStart: &stale}, now, lastSent)); n != 1 {
			t.Fatalf("first eval: expected 1 alert, got %d", n)
		}
		later := now.Add(1 * time.Hour)
		if n := len(evaluatePipelineHealth(PipelineHealth{LastOKStart: &stale}, later, lastSent)); n != 0 {
			t.Fatalf("within cooldown: expected 0 alerts, got %d", n)
		}
		if n := len(evaluatePipelineHealth(PipelineHealth{LastOKStart: &stale, StuckRunning: 1}, later, lastSent)); n != 1 {
			t.Fatalf("new condition within cooldown: expected 1 alert (stuck only), got %d", n)
		}
		afterCooldown := now.Add(25 * time.Hour)
		if n := len(evaluatePipelineHealth(PipelineHealth{LastOKStart: &stale}, afterCooldown, lastSent)); n != 1 {
			t.Fatalf("after cooldown: expected 1 alert, got %d", n)
		}
	})
}
