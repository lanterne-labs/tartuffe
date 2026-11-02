package backend

import (
	"testing"
	"time"
)

func TestParseStatementDate(t *testing.T) {
	cases := []struct{ in, iso, long string }{
		{"2019", "2019", "2019"},
		{"2024-03", "2024-03", "March 2024"},
		{"2023-07-14", "2023-07-14", "July 14, 2023"},
	}
	for _, c := range cases {
		d, err := ParseStatementDate(c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.in, err)
		}
		if got := FormatFactDateISO(&d); got != c.iso {
			t.Errorf("%s: iso %q, want %q", c.in, got, c.iso)
		}
		if got := FormatFactDate(&d); got != c.long {
			t.Errorf("%s: long %q, want %q", c.in, got, c.long)
		}
	}
	for _, bad := range []string{"", "19", "2024-13", "2023-02-30", "2024/03/01", "mars 2024"} {
		if _, err := ParseStatementDate(bad); err == nil {
			t.Errorf("%q: want an error", bad)
		}
	}
}

// A statement date sorts and subtracts like any date: the precision is a
// zone name at offset 0.
func TestStatementDateCompares(t *testing.T) {
	year, _ := ParseStatementDate("2019")
	source := time.Date(2025, 11, 9, 10, 0, 0, 0, time.UTC)
	if !year.Before(source) {
		t.Fatal("2019 must come before the 2025 source")
	}
	if !year.Equal(time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("year statement date %v, want 2019-01-01 UTC", year)
	}
	if FormatFactDate(&source) != "November 9, 2025" || FormatFactDateISO(&source) != "2025-11-09" {
		t.Errorf("a source date prints in full: %q %q", FormatFactDate(&source), FormatFactDateISO(&source))
	}
	if FormatFactDate(nil) != "unknown date" || FormatFactDateISO(nil) != "" {
		t.Error("a missing date prints as unknown")
	}
}

func TestFactDate(t *testing.T) {
	source := time.Date(2025, 11, 9, 0, 0, 0, 0, time.UTC)
	if got := FactDate(nil, nil, &source); got != &source {
		t.Errorf("no statement date: want the source's, got %v", got)
	}
	st := time.Date(2019, 3, 1, 0, 0, 0, 0, time.UTC)
	p := DatePrecisionMonth
	got := FactDate(&st, &p, &source)
	if got == nil || FormatFactDateISO(got) != "2019-03" {
		t.Errorf("statement date: got %v, want 2019-03", got)
	}
}
