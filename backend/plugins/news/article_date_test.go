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
