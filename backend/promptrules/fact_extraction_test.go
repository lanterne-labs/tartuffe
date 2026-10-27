package promptrules

import (
	"strings"
	"testing"
	"time"
)

func TestBuildFactExtractionPrompt(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	base := FactExtractorConfig{
		TargetName: "Marine Le Pen",
		SourceURL:  "https://example.com/article",
		Content:    "Article body with 100% and a \"quote\".",
		Now:        now,
	}

	plain := BuildFactExtractionPrompt(base)
	for _, want := range []string{
		"REFERENCE DATE: 2026-09-16",
		"Focus especially on the primary subject 'Marine Le Pen'",
		"Primary Subject: Marine Le Pen\nSource URL: https://example.com/article\nContent:\nArticle body with 100% and a \"quote\".",
	} {
		if !strings.Contains(plain, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	if strings.Contains(plain, "## Additional Context") {
		t.Fatal("plugin section rendered without plugin instructions")
	}

	withPlugin := base
	withPlugin.PluginInstructions = PartyInstructions
	got := BuildFactExtractionPrompt(withPlugin)
	if !strings.Contains(got, "## Additional Context\n"+PartyInstructions+"\n\n## Input Data") {
		t.Fatal("plugin instructions not rendered between the rules and the input data")
	}

	if !strings.HasSuffix(got, "All other rules are identical.") {
		t.Fatal("response-format block must close the prompt")
	}
}
