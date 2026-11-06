package twitter

import (
	"context"
	"testing"
	"time"

	"digital-mirror/backend/categorizer"
	"digital-mirror/backend/llmtest"
	"digital-mirror/backend/promptrules"
)

// TestTweetAuthorGolden checks that every fact of a tweet has the tweet's
// author as its subject. Audit 2026-11-06: 189 of 28625 tweet facts were
// filed under another person, mostly the party the author speaks for
// (Mélenchon's « La France insoumise demande… », 33) or the person the tweet
// quotes (Zemmour's tweets on Laurent Nuñez, 10). The two tweets are written
// for the test in the shape of those cases.
//
// LOCAL ONLY: it skips when no LLM is configured.
func TestTweetAuthorGolden(t *testing.T) {
	llmtest.Setup(t)

	ex, err := categorizer.NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatalf("NewOpenAIExtractorFromEnv: %v", err)
	}
	at := time.Date(2026, 10, 20, 9, 30, 0, 0, time.UTC)
	cases := []struct {
		person, handle, text string
	}{
		{"Éric Zemmour", "ZemmourEric", "Laurent Nuñez a reconnu hier devant le Sénat que 40 % des OQTF ne sont jamais exécutées. Le ministre avoue l'échec de l'État. Il faut expulser, enfin."},
		{"Jean-Luc Mélenchon", "JLMelenchon", "La France insoumise demande le blocage des prix de l'énergie et votera contre ce budget. Nous déposerons une motion de censure dès lundi."},
	}
	for _, c := range cases {
		t.Run(c.handle, func(t *testing.T) {
			tw := Tweet{ID: "1", URL: "https://x.com/" + c.handle + "/status/1", AuthorHandle: c.handle, AuthorName: c.person, Text: c.text, CreatedAt: &at, Lang: "fr"}
			facts, err := ex.ExtractFactsWithConfig(context.Background(), promptrules.FactExtractorConfig{
				TargetName:         c.person,
				SourceURL:          "https://x.com/" + c.handle,
				Content:            formatTweetBatch([]Tweet{tw}),
				PluginInstructions: pluginInstructions,
			})
			if err != nil {
				t.Fatalf("ExtractFactsWithConfig: %v", err)
			}
			if len(facts) == 0 {
				t.Fatal("no fact from a tweet that states positions")
			}
			for i, f := range facts {
				t.Logf("  [%d] subject=%q fact=%q", i+1, f.SubjectEntity, f.NormalizedFactText)
				if !isAuthor(f.SubjectEntity, c.person) {
					t.Errorf("fact %d has subject %q, not the author %q: %q", i+1, f.SubjectEntity, c.person, f.NormalizedFactText)
				}
			}
		})
	}
}
