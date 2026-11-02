package core

import (
	"testing"
	"time"
)

// TestStanceJudgesSeeDates: the analyze prompt shows each key fact's date
// before its text, "(undated)" when unknown, and the verifier gets the same
// date on its statement (until 2026-10-27 neither saw dates: two budgets a
// year apart were judged as one text, mirror-ds6.11).
func TestStanceJudgesSeeDates(t *testing.T) {
	d := time.Date(2025, 10, 24, 12, 0, 0, 0, time.UTC)
	dated := StanceFactInfo{ID: 131586, CanonicalVerb: "ANNOUNCED", NormalizedFactText: "Le RN votera contre le budget.", Date: &d}
	undated := StanceFactInfo{ID: 7, CanonicalVerb: "CLAIMED", NormalizedFactText: "Sans date."}

	if got, want := formatKeyFactLine(dated), "- [ID 131586] (ANNOUNCED) (dated 2025-10-24) Le RN votera contre le budget.\n"; got != want {
		t.Errorf("dated line = %q, want %q", got, want)
	}
	if got, want := formatKeyFactLine(undated), "- [ID 7] (CLAIMED) (undated) Sans date.\n"; got != want {
		t.Errorf("undated line = %q, want %q", got, want)
	}
	if got := statementOf(dated); got.Text != dated.NormalizedFactText || got.Date != "2025-10-24" {
		t.Errorf("statementOf(dated) = %+v", got)
	}
	if got := statementOf(undated); got.Date != "" {
		t.Errorf("statementOf(undated) has a date: %+v", got)
	}
	m := keyFactByID([]StanceFactInfo{dated, undated})
	if m[131586].Date != &d || m[7].NormalizedFactText != "Sans date." {
		t.Errorf("keyFactByID lost a fact: %+v", m)
	}
}
