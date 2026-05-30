package news

import (
	"testing"
	"time"
)

func date(year, month, day int) time.Time {
	return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
}

func TestComputeReQueryWindow(t *testing.T) {
	tests := []struct {
		name        string
		windowFrom  time.Time
		windowTo    time.Time
		cappedDates []time.Time
		wantNil     bool
		wantFrom    time.Time
		wantTo      time.Time
	}{
		{
			name:        "empty capped dates returns nil",
			windowFrom:  date(2025, 1, 1),
			windowTo:    date(2025, 1, 31),
			cappedDates: nil,
			wantNil:     true,
		},
		{
			name:        "cluster at start, picks after gap",
			windowFrom:  date(2025, 1, 1),
			windowTo:    date(2025, 1, 31),
			cappedDates: []time.Time{date(2025, 1, 2), date(2025, 1, 3)},
			// cluster: Jan 1 - Jan 4 (with buffer), after gap: Jan 4 - Jan 31 (27 days)
			wantNil:  false,
			wantFrom: date(2025, 1, 4),
			wantTo:   date(2025, 1, 31),
		},
		{
			name:        "cluster at end, picks before gap",
			windowFrom:  date(2025, 1, 1),
			windowTo:    date(2025, 1, 31),
			cappedDates: []time.Time{date(2025, 1, 28), date(2025, 1, 29)},
			// cluster: Jan 27 - Jan 30 (with buffer), before gap: Jan 1 - Jan 27 (26 days)
			wantNil:  false,
			wantFrom: date(2025, 1, 1),
			wantTo:   date(2025, 1, 27),
		},
		{
			name:        "cluster in middle, picks larger gap",
			windowFrom:  date(2025, 1, 1),
			windowTo:    date(2025, 1, 31),
			cappedDates: []time.Time{date(2025, 1, 10)},
			// cluster: Jan 9 - Jan 11 (with buffer)
			// before gap: Jan 1 - Jan 9 (8 days)
			// after gap: Jan 11 - Jan 31 (20 days) — larger
			wantNil:  false,
			wantFrom: date(2025, 1, 11),
			wantTo:   date(2025, 1, 31),
		},
		{
			name:        "cluster spans entire window, returns nil",
			windowFrom:  date(2025, 1, 1),
			windowTo:    date(2025, 1, 5),
			cappedDates: []time.Time{date(2025, 1, 1), date(2025, 1, 5)},
			// cluster: Dec 31 - Jan 6 (with buffer), both gaps < 0
			wantNil: true,
		},
		{
			name:        "cluster at window boundary, clamps correctly",
			windowFrom:  date(2025, 1, 1),
			windowTo:    date(2025, 1, 31),
			cappedDates: []time.Time{date(2025, 1, 1), date(2025, 1, 2)},
			// cluster start would be Dec 31 but clamped to Jan 1
			// cluster end is Jan 3 (with buffer)
			// before gap: Jan 1 - Jan 1 = 0 (too small)
			// after gap: Jan 3 - Jan 31 = 28 days
			wantNil:  false,
			wantFrom: date(2025, 1, 3),
			wantTo:   date(2025, 1, 31),
		},
		{
			name:        "single capped date, all on same day",
			windowFrom:  date(2025, 1, 1),
			windowTo:    date(2025, 1, 31),
			cappedDates: []time.Time{date(2025, 1, 15)},
			// cluster: Jan 14 - Jan 16 (with buffer)
			// before gap: Jan 1 - Jan 14 (13 days)
			// after gap: Jan 16 - Jan 31 (15 days) — larger
			wantNil:  false,
			wantFrom: date(2025, 1, 16),
			wantTo:   date(2025, 1, 31),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeReQueryWindow(tt.windowFrom, tt.windowTo, tt.cappedDates)
			if tt.wantNil {
				if got != nil {
					t.Errorf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatal("expected non-nil window, got nil")
			}
			if !got.from.Equal(tt.wantFrom) {
				t.Errorf("from = %s, want %s", got.from.Format("2006-01-02"), tt.wantFrom.Format("2006-01-02"))
			}
			if !got.to.Equal(tt.wantTo) {
				t.Errorf("to = %s, want %s", got.to.Format("2006-01-02"), tt.wantTo.Format("2006-01-02"))
			}
		})
	}
}

func TestContainsString(t *testing.T) {
	if containsString([]string{"a", "b", "c"}, "b") != true {
		t.Error("expected true for existing element")
	}
	if containsString([]string{"a", "b", "c"}, "d") != false {
		t.Error("expected false for missing element")
	}
	if containsString(nil, "a") != false {
		t.Error("expected false for nil slice")
	}
}
