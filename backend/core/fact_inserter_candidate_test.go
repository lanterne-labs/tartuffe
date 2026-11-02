package core

import (
	"context"
	"testing"
	"time"
)

// candidateStorer is fakeInserterStorer with the candidate flag: person 1 is
// a candidate, every other person is not.
type candidateStorer struct {
	fakeInserterStorer
	asked int
}

func (c *candidateStorer) IsCandidate(ctx context.Context, personID int64) (bool, error) {
	c.asked++
	return personID == 1, nil
}

func (c *candidateStorer) FindOrInsertPersons(ctx context.Context, names []string) ([]int64, error) {
	if names[0] == "Other" {
		return []int64{2}, nil
	}
	return []int64{1}, nil
}

type countingStanceProcessor struct{ calls int }

func (p *countingStanceProcessor) ProcessNewFact(ctx context.Context, personID, factID int64, factText string, embedding []float32, verb string, _ *time.Time) (*StanceProcessResult, error) {
	p.calls++
	return &StanceProcessResult{Verdict: "NEW_STANCE", StanceID: 1}, nil
}

// TestCandidateGate checks that a fact of a person who is not a candidate is
// stored without a stance, that a candidate's fact gets one, and that the
// flag is read once per person.
func TestCandidateGate(t *testing.T) {
	st := &candidateStorer{}
	sp := &countingStanceProcessor{}
	fi := NewFactInserter(st, sp)
	ctx := context.Background()

	fact := func(subject string) StoreFactInput {
		return StoreFactInput{SubjectEntity: subject, NormalizedFactText: subject + " a dit quelque chose", SourceType: "news", OriginalQuote: "quelque chose de long à citer", PublishedAt: &testDate}
	}
	res, err := fi.InsertFacts(ctx, []StoreFactInput{fact("Other"), fact("Other"), fact("Candidate")})
	if err != nil {
		t.Fatal(err)
	}
	if res.Inserted != 3 || st.inserted != 3 {
		t.Fatalf("inserted %d (storer %d), want 3: every person's facts are stored", res.Inserted, st.inserted)
	}
	if sp.calls != 1 {
		t.Errorf("stance processor called %d times, want 1: the candidate's fact only", sp.calls)
	}
	if st.deleted != 0 {
		t.Errorf("%d facts deleted, want 0", st.deleted)
	}
	if st.asked != 2 {
		t.Errorf("candidate flag read %d times, want 2 (once per person)", st.asked)
	}
}

// TestNoCandidateStorer checks that a storer without the flag processes
// every person (the tests' fakes).
func TestNoCandidateStorer(t *testing.T) {
	st := &fakeInserterStorer{}
	sp := &countingStanceProcessor{}
	fi := NewFactInserter(st, sp)
	_, err := fi.InsertFacts(context.Background(), []StoreFactInput{{SubjectEntity: "Anyone", NormalizedFactText: "x", SourceType: "news", OriginalQuote: "a quote that is long enough", PublishedAt: &testDate}})
	if err != nil {
		t.Fatal(err)
	}
	if sp.calls != 1 {
		t.Errorf("stance processor called %d times, want 1", sp.calls)
	}
}
