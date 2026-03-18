package frpolitics

import (
	"testing"
	"time"
)

func TestCurrentLegislatureAt(t *testing.T) {
	tests := []struct {
		name string
		date time.Time
		want int
	}{
		{"during 17th legislature", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 17},
		{"start of 17th legislature", time.Date(2024, 7, 18, 0, 0, 0, 0, time.UTC), 17},
		{"day before 17th", time.Date(2024, 7, 17, 0, 0, 0, 0, time.UTC), 16},
		{"during 16th legislature", time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC), 16},
		{"start of 16th legislature", time.Date(2022, 6, 28, 0, 0, 0, 0, time.UTC), 16},
		{"during 15th legislature", time.Date(2020, 3, 15, 0, 0, 0, 0, time.UTC), 15},
		{"during 14th legislature", time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC), 14},
		{"before all known legislatures", time.Date(2010, 1, 1, 0, 0, 0, 0, time.UTC), 14},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := currentLegislatureAt(tt.date)
			if got != tt.want {
				t.Errorf("currentLegislatureAt(%v) = %d, want %d", tt.date, got, tt.want)
			}
		})
	}
}

func TestCurrentLegislature(t *testing.T) {
	// As of 2026, should return 17
	got := currentLegislature()
	if got != 17 {
		t.Errorf("currentLegislature() = %d, want 17", got)
	}
}
