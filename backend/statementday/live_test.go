package statementday

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"digital-mirror/backend"
	"digital-mirror/backend/extractor"
)

// TestLive runs the finder on real statements with the real search and LLM:
// a measure, not a check. STATEMENTDAY_LIVE names a JSON file of
// {"id", "person", "text", "known": "YYYY-MM-DD", "precision"}; each line of
// the output is a statement and the day found, to review by hand.
func TestLive(t *testing.T) {
	path := os.Getenv("STATEMENTDAY_LIVE")
	if path == "" {
		t.Skip("STATEMENTDAY_LIVE not set")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var facts []struct {
		ID        int64  `json:"id"`
		Person    string `json:"person"`
		Text      string `json:"text"`
		Known     string `json:"known"`
		Precision string `json:"precision"`
	}
	if err := json.Unmarshal(raw, &facts); err != nil {
		t.Fatal(err)
	}
	pages, err := extractor.NewExtractorFromType("auto")
	if err != nil {
		t.Fatal(err)
	}
	finder, err := NewFinderFromEnv(ExtractorReader(pages))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, f := range facts {
		known, err := time.Parse("2006-01-02", f.Known)
		if err != nil {
			t.Fatal(err)
		}
		got, err := finder.Find(context.Background(), Statement{Person: f.Person, Text: f.Text, Known: backend.WithDatePrecision(known, f.Precision)})
		if err != nil {
			t.Errorf("fact %d: %v", f.ID, err)
			continue
		}
		if got.Day != nil {
			found++
		}
		article := "-"
		if page := articleOf(got); page != nil {
			article = fmt.Sprintf("%d runes, published %s", len([]rune(page.Text)), backend.FormatFactDateISO(page.Published))
		}
		t.Logf("fact %d (%s, %s) %s\n  day: %s\n  url: %s\n  evidence: %s\n  article to queue: %s\n  why: %s", f.ID, f.Person, f.Known, f.Text, backend.FormatFactDateISO(got.Day), got.URL, got.Evidence, article, got.Reasoning)
	}
	t.Logf("%d of %d statements got a day", found, len(facts))
}
