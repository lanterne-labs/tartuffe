package categorizer

import (
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/extractor"
	"digital-mirror/backend/promptrules"
)

// TestContentConfigReferenceDate: the prompt of a scraped page takes the
// page's publication date as its reference date, so "lundi" in a September
// article read in October is the Monday of September (fact 91591,
// mirror-ds6.9). Without a publication date the prompt falls back to today.
func TestContentConfigReferenceDate(t *testing.T) {
	published := time.Date(2026, 9, 29, 10, 0, 0, 0, time.UTC)
	content := extractor.ExtractedContent{URL: "https://example.com/a", Content: "lundi, elle a dit", DatePublished: &published}

	cfg := contentConfig("Marine Le Pen", content, "notes")
	if !cfg.Now.Equal(published) {
		t.Fatalf("reference date = %v, want the publication date %v", cfg.Now, published)
	}
	if cfg.PluginInstructions != "notes" || cfg.SourceURL != content.URL || cfg.Content != content.Content {
		t.Fatalf("the config lost a field: %+v", cfg)
	}
	prompt := promptrules.BuildFactExtractionPrompt(cfg)
	if !strings.Contains(prompt, "REFERENCE DATE: 2026-09-29") {
		t.Fatalf("the prompt does not carry the publication date as reference date")
	}

	zero := time.Time{}
	for _, c := range []extractor.ExtractedContent{{URL: "u", Content: "c"}, {URL: "u", Content: "c", DatePublished: &zero}} {
		if got := contentConfig("x", c, "").Now; !got.IsZero() {
			t.Fatalf("without a publication date the reference date must be unset (today at prompt time), got %v", got)
		}
	}
}
