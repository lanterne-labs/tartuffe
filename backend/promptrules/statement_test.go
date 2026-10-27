package promptrules

import (
	"testing"
	"time"
)

// TestStatementDated: the verifier sees a statement's date before its text,
// "(undated)" when the date is unknown; Render (the auditor's input, hashed)
// never shows it.
func TestStatementDated(t *testing.T) {
	s := Statement{Text: "Elle votera contre le budget.", Date: "2025-10-24"}
	if got, want := s.Dated(), `(dated 2025-10-24) "Elle votera contre le budget."`; got != want {
		t.Errorf("Dated() = %q, want %q", got, want)
	}
	if got, want := s.Render(), `"Elle votera contre le budget."`; got != want {
		t.Errorf("Render() shows the date: %q, want %q", got, want)
	}
	u := Statement{Text: "x", Also: []string{"y"}}
	if got, want := u.Dated(), "(undated) \"x\"\n  Other wordings of the same statement: \"y\""; got != want {
		t.Errorf("undated Dated() = %q, want %q", got, want)
	}

	d := time.Date(2025, 10, 24, 23, 30, 0, 0, time.FixedZone("CEST", 2*3600))
	if got := DateOf(&d); got != "2025-10-24" {
		t.Errorf("DateOf = %q, want the UTC day 2025-10-24", got)
	}
	zero := time.Time{}
	if DateOf(nil) != "" || DateOf(&zero) != "" {
		t.Errorf("DateOf of nil or zero must be empty")
	}
}
