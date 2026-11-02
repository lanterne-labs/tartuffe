package core

import (
	"testing"
	"time"

	"digital-mirror/backend"
)

// A statement date that only repeats the source's date is not stored: the
// judges keep the source's full date.
func TestSameStatementDay(t *testing.T) {
	source := time.Date(2025, 11, 9, 10, 32, 0, 0, time.UTC)
	cases := []struct {
		date string
		same bool
	}{
		{"2025", true},
		{"2025-11", true},
		{"2025-11-09", true},
		{"2019", false},
		{"2025-10", false},
		{"2025-11-08", false},
	}
	for _, c := range cases {
		d, err := backend.ParseStatementDate(c.date)
		if err != nil {
			t.Fatal(err)
		}
		if got := SameStatementDay(&d, &source); got != c.same {
			t.Errorf("%s vs %v: same %v, want %v", c.date, source, got, c.same)
		}
	}
	if SameStatementDay(nil, &source) {
		t.Error("no statement date is not the source's")
	}
}
