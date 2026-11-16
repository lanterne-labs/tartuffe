package core

import (
	"context"
	"testing"
	"time"

	"digital-mirror/backend"
)

type recordingDayFinder struct{ asked []string }

func (r *recordingDayFinder) FindStatementDay(_ context.Context, _, _ int64, person, _, _ string, known time.Time, _ *time.Time) error {
	r.asked = append(r.asked, person+" "+backend.FormatFactDateISO(&known))
	return nil
}

// The day of a statement dated to a year or a month is searched the moment
// the fact is stored, for a candidate only; a fact dated to the day or by its
// source is not searched.
func TestStatementDaySearchedAtInsertion(t *testing.T) {
	finder := &recordingDayFinder{}
	UseStatementDays(finder)
	defer UseStatementDays(nil)

	fi := NewFactInserter(&candidateStorer{}, &countingStanceProcessor{})
	date := func(s string) *time.Time {
		d, err := backend.ParseStatementDate(s)
		if err != nil {
			t.Fatal(err)
		}
		return &d
	}
	fact := func(subject string, statement *time.Time) StoreFactInput {
		return StoreFactInput{SubjectEntity: subject, NormalizedFactText: subject + " a dit quelque chose", SourceType: "news",
			OriginalQuote: "quelque chose de long à citer", PublishedAt: &testDate, StatementDate: statement}
	}
	_, err := fi.InsertFacts(context.Background(), []StoreFactInput{
		fact("Candidate", date("2013")),
		fact("Candidate", date("2022-03")),
		fact("Candidate", date("2021-12-07")),
		fact("Candidate", nil),
		fact("Other", date("2013")),
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Candidate 2013", "Candidate 2022-03"}
	if len(finder.asked) != len(want) || finder.asked[0] != want[0] || finder.asked[1] != want[1] {
		t.Fatalf("searched %v, want %v", finder.asked, want)
	}
}
