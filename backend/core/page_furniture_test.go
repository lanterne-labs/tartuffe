package core

import (
	"context"
	"testing"
	"time"
)

// furnitureStorer is the inserter fake with the page-furniture check: seen
// answers QuoteOnPageOfOtherDate.
type furnitureStorer struct {
	fakeInserterStorer
	seen  bool
	calls int
}

func (f *furnitureStorer) QuoteOnPageOfOtherDate(ctx context.Context, personID int64, quote, sourceURL string, publishedAt *time.Time, gap time.Duration) (bool, error) {
	f.calls++
	return f.seen, nil
}

// TestPageFurnitureGuard: a news quote the person already has from a page of
// another date is skipped without aborting the batch; a vote record is never
// checked (mirror-d3a).
func TestPageFurnitureGuard(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sourceType string
		seen       bool
		wantInsert bool
		wantCalls  int
	}{
		{"news quote seen on a page of another date: skipped", "news", true, false, 1},
		{"news quote new: inserted", "news", false, true, 1},
		{"vote record: not checked", "assemblee_law_page", true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := &furnitureStorer{seen: tc.seen}
			fi := NewFactInserter(st, fakeStanceProcessor{})
			res, err := fi.InsertFacts(context.Background(), []StoreFactInput{{
				SubjectEntity: "Test Person", NormalizedFactText: "Test Person a déclaré X.",
				OriginalQuote: "Pillages : Test Person demande aux plateformes de coopérer",
				SourceType:    tc.sourceType, SourceURL: "https://example.test/old-page",
			}})
			if err != nil {
				t.Fatalf("InsertFacts: %v", err)
			}
			if got := st.inserted == 1; got != tc.wantInsert {
				t.Errorf("inserted=%d skipped=%d, want insert %v", st.inserted, res.Skipped, tc.wantInsert)
			}
			if st.calls != tc.wantCalls {
				t.Errorf("checked %d times, want %d", st.calls, tc.wantCalls)
			}
		})
	}
}
