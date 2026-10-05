package core

import (
	"context"
	"testing"
	"time"
)

// fakeInserterStorer is a minimal Storer for exercising InsertFacts /
// insertSingleFact without a database. It records how many facts actually
// reached InsertFact so the empty-quote guard can be asserted: a guarded fact
// must be rejected before it ever gets that far.
type fakeInserterStorer struct {
	inserted   int
	deleted    int
	nextFactID int64
	nextSrcID  int64
}

func (f *fakeInserterStorer) FindOrInsertPersons(ctx context.Context, names []string) ([]int64, error) {
	return []int64{1}, nil
}

func (f *fakeInserterStorer) FindOrInsertPersonWithType(ctx context.Context, name, entityType string) (int64, error) {
	return 1, nil
}

func (f *fakeInserterStorer) FindSimilarActiveFacts(ctx context.Context, q SimilarFactQuery) ([]SimilarFact, error) {
	return nil, nil
}

func (f *fakeInserterStorer) InsertFact(ctx context.Context, input StoreFactInput) (int64, error) {
	f.inserted++
	f.nextFactID++
	return f.nextFactID, nil
}

func (f *fakeInserterStorer) DeleteFact(ctx context.Context, factID int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.deleted++
	return nil
}

func (f *fakeInserterStorer) LinkSourceToFact(ctx context.Context, factID int64, sourceURL, originalQuote string, publishedAt *time.Time, sourceType string, metadata map[string]any) (int64, error) {
	f.nextSrcID++
	return f.nextSrcID, nil
}

func (f *fakeInserterStorer) LinkPersonsToSourceMention(ctx context.Context, sourceID int64, personIDs []int64) error {
	return nil
}

// fakeStanceProcessor satisfies the mandatory stance routing without an LLM.
type fakeStanceProcessor struct{}

func (fakeStanceProcessor) ProcessNewFact(ctx context.Context, personID, factID int64, factText string, embedding []float32, verb string) (*StanceProcessResult, error) {
	return &StanceProcessResult{Verdict: "NEW_STANCE", StanceID: 1}, nil
}

func newTestInserter() (*FactInserter, *fakeInserterStorer) {
	st := &fakeInserterStorer{}
	return NewFactInserter(st, fakeStanceProcessor{}), st
}

// TestEmptyQuoteGuard checks that web-extracted facts (news/twitter) with a blank
// quote are rejected before insertion, while structured sources and facts with a
// real quote pass through.
func TestEmptyQuoteGuard(t *testing.T) {
	cases := []struct {
		name       string
		sourceType string
		quote      string
		wantInsert bool
	}{
		{"news empty quote skipped", "news", "", false},
		{"twitter empty quote skipped", "twitter", "", false},
		{"news whitespace quote skipped", "news", "   \n\t ", false},
		{"news with quote inserted", "news", "exact snippet from article", true},
		{"vote empty quote inserted", "assemblee_law_page", "", true},
		{"debate empty quote skipped", "assemblee_debat", "", false},
		{"debate with quote inserted", "assemblee_debat", "je le dis ici", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fi, st := newTestInserter()
			res, err := fi.InsertFacts(context.Background(), []StoreFactInput{{
				SubjectEntity:      "Test Person",
				NormalizedFactText: "Test Person a fait une déclaration.",
				OriginalQuote:      tc.quote,
				SourceType:         tc.sourceType,
				SourceURL:          "https://example.test/a",
			}})
			if err != nil {
				t.Fatalf("InsertFacts returned error: %v", err)
			}
			if tc.wantInsert {
				if res.Inserted != 1 || st.inserted != 1 {
					t.Fatalf("expected fact inserted, got Inserted=%d storer.inserted=%d Skipped=%d", res.Inserted, st.inserted, res.Skipped)
				}
			} else {
				if res.Skipped != 1 || res.Inserted != 0 || st.inserted != 0 {
					t.Fatalf("expected fact skipped before insert, got Inserted=%d storer.inserted=%d Skipped=%d", res.Inserted, st.inserted, res.Skipped)
				}
			}
		})
	}
}

// TestEmptyQuoteGuard_DoesNotAbortBatch: a bad (empty-quote) fact must be skipped
// without aborting the rest of the batch.
func TestEmptyQuoteGuard_DoesNotAbortBatch(t *testing.T) {
	fi, st := newTestInserter()
	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{
		{SubjectEntity: "A", NormalizedFactText: "A empty quote", OriginalQuote: "", SourceType: "news", SourceURL: "u1"},
		{SubjectEntity: "B", NormalizedFactText: "B good quote", OriginalQuote: "snippet", SourceType: "news", SourceURL: "u2"},
	})
	if err != nil {
		t.Fatalf("batch should not error: %v", err)
	}
	if res.Skipped != 1 || res.Inserted != 1 || st.inserted != 1 {
		t.Fatalf("want 1 skipped + 1 inserted, got Skipped=%d Inserted=%d storer.inserted=%d", res.Skipped, res.Inserted, st.inserted)
	}
}
