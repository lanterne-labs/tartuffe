package core

import (
	"context"
	"testing"
)

// A fact a person removed from a source is not stored again when the source
// is extracted again (prod 2479); another fact of the same source is.
func TestRemovedFactIsNotStoredAgain(t *testing.T) {
	var asked []string
	UseRemovedFacts(func(_ context.Context, url, text, _ string) (bool, error) {
		asked = append(asked, url)
		return text == "Test Person est favorable à la retraite à 60 ans.", nil
	})
	defer UseRemovedFacts(nil)

	st := &furnitureStorer{}
	fi := NewFactInserter(st, fakeStanceProcessor{})
	fact := func(text string) StoreFactInput {
		return StoreFactInput{SubjectEntity: "Test Person", NormalizedFactText: text, OriginalQuote: "une citation assez longue",
			SourceType: "news", SourceURL: "https://example.test/programme", PublishedAt: &testDate}
	}
	res, err := fi.InsertFacts(context.Background(), []StoreFactInput{
		fact("Test Person est favorable à la retraite à 60 ans."),
		fact("Test Person veut rétablir l'impôt sur la fortune."),
	})
	if err != nil {
		t.Fatalf("InsertFacts: %v", err)
	}
	if st.inserted != 1 || res.Skipped != 1 || len(asked) != 2 {
		t.Fatalf("inserted=%d skipped=%d asked=%d, want 1, 1, 2", st.inserted, res.Skipped, len(asked))
	}
}
