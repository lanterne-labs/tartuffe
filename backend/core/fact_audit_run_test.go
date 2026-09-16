package core

import (
	"context"
	"errors"
	"sort"
	"testing"
)

// fakeAuditor returns a verdict keyed by the fact text.
type fakeAuditor struct{ verdicts map[string]FactAuditResult }

func (f fakeAuditor) Audit(ctx context.Context, in FactAuditInput) FactAuditResult {
	return f.verdicts[in.NormalizedFact]
}

// Parallel workers must deliver every outcome exactly once, tally verdicts and
// errors correctly, and collect the delete ids.
func TestRunFactAudit_TallyAndDelivery(t *testing.T) {
	auditor := fakeAuditor{verdicts: map[string]FactAuditResult{
		"k1": {Verdict: FactVerdictKeep, Rule: "none"},
		"k2": {Verdict: FactVerdictKeep, Rule: "none"},
		"d1": {Verdict: FactVerdictDelete, Rule: "vague"},
		"u1": {Verdict: FactVerdictUnsure, Rule: "speculative"},
		"e1": {Err: errors.New("boom")},
	}}
	var jobs []FactAuditJob
	for i, text := range []string{"k1", "k2", "d1", "u1", "e1"} {
		jobs = append(jobs, FactAuditJob{FactID: int64(i + 1), Input: FactAuditInput{NormalizedFact: text}})
	}

	var seen []int64
	tally, err := RunFactAudit(context.Background(), auditor, jobs, 3, func(o FactAuditOutcome) error {
		seen = append(seen, o.FactID)
		return nil
	})
	if err != nil {
		t.Fatalf("RunFactAudit: %v", err)
	}
	sort.Slice(seen, func(i, j int) bool { return seen[i] < seen[j] })
	if len(seen) != 5 || seen[0] != 1 || seen[4] != 5 {
		t.Fatalf("every job must be delivered exactly once, got %v", seen)
	}
	if tally.Keep != 2 || tally.Delete != 1 || tally.Unsure != 1 || tally.Errors != 1 {
		t.Fatalf("tally = %+v", tally)
	}
	if len(tally.DeleteIDs) != 1 || tally.DeleteIDs[0] != 3 {
		t.Fatalf("delete ids = %v, want [3]", tally.DeleteIDs)
	}
}

// An onResult error aborts the run and surfaces.
func TestRunFactAudit_OnResultErrorAborts(t *testing.T) {
	auditor := fakeAuditor{verdicts: map[string]FactAuditResult{"x": {Verdict: FactVerdictKeep, Rule: "none"}}}
	jobs := []FactAuditJob{{FactID: 1, Input: FactAuditInput{NormalizedFact: "x"}}}
	want := errors.New("encode failed")
	_, err := RunFactAudit(context.Background(), auditor, jobs, 1, func(FactAuditOutcome) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("expected the onResult error to surface, got %v", err)
	}
}

func TestFactAuditSubjectLabel(t *testing.T) {
	if got := FactAuditSubjectLabel(nil); got != "" {
		t.Errorf("no persons: got %q", got)
	}
	if got := FactAuditSubjectLabel([]string{"a"}); got != "a" {
		t.Errorf("one person: got %q", got)
	}
	if got := FactAuditSubjectLabel([]string{"a", "b", "c"}); got != "a (+2 more)" {
		t.Errorf("three persons: got %q", got)
	}
}
