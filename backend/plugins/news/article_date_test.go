package news

import (
	"testing"
	"time"

	"digital-mirror/backend"
	"digital-mirror/backend/core"
)

// The document's date wins over the LLM's reading of the body; the LLM's
// date serves only a document without one.
func TestArticleDatePrefersTheDocument(t *testing.T) {
	docDate := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	llmDate := time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)
	facts := []backend.Fact{{ArticlePublishedAt: &llmDate}}

	if got := articleDate(core.SourceDocument{PublishedAt: &docDate}, facts); got == nil || !got.Equal(docDate) {
		t.Errorf("with a document date: got %v, want %v", got, docDate)
	}
	if got := articleDate(core.SourceDocument{}, facts); got == nil || !got.Equal(llmDate) {
		t.Errorf("without a document date: got %v, want %v", got, llmDate)
	}
}

// A page found by a search over an older window, with no date of its own,
// does not take an LLM date after that window: it stays undated.
func TestArticleDateDropsAnLLMDateAfterTheSearchWindow(t *testing.T) {
	llmDate := time.Date(2026, 10, 29, 0, 0, 0, 0, time.UTC)
	facts := []backend.Fact{{ArticlePublishedAt: &llmDate}}
	old := core.SourceDocument{Metadata: map[string]any{"search_to": "2023-06-30T00:00:00Z"}}
	if got := articleDate(old, facts); got != nil {
		t.Errorf("after an old window: got %v, want undated", got)
	}
	recent := core.SourceDocument{Metadata: map[string]any{"search_to": "2026-10-28T00:00:00Z"}}
	if got := articleDate(recent, facts); got == nil || !got.Equal(llmDate) {
		t.Errorf("inside the window's slack: got %v, want %v", got, llmDate)
	}
	docDate := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	old.PublishedAt = &docDate
	if got := articleDate(old, facts); got == nil || !got.Equal(docDate) {
		t.Errorf("the document date still wins: got %v, want %v", got, docDate)
	}
}
