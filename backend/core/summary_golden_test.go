package core

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"
	"unicode"
	"unicode/utf8"

	"digital-mirror/backend/llmtest"

	"gopkg.in/yaml.v3"
)

// summaryCase is one row of testdata/summary_cases.yaml: a real stance of an
// audit, with what its summary did wrong then.
type summaryCase struct {
	Name        string           `yaml:"name"`
	StanceID    int64            `yaml:"stance_id"`
	Person      string           `yaml:"person"`
	Label       string           `yaml:"label"`
	Facts       []analyzeKeyFact `yaml:"facts"`
	NoOrdering  bool             `yaml:"no_ordering"`  // the facts are undated: no order in time
	MustMention []string         `yaml:"must_mention"` // words the summary has to carry (case-insensitive)
	Notes       string           `yaml:"notes"`
}

type summaryCaseFile struct {
	Cases []summaryCase `yaml:"cases"`
}

// The wordings a summary may not use (stanceSummaryRules): a comment on the
// list of facts, a judgement of tension, an order in time between undated
// facts.
var (
	summaryMetaRe    = regexp.MustCompile(`(?i)les faits (listés|relevés|ci-dessus|présentés|disponibles)|les éléments (ci-dessus|listés|relevés)|la liste (des faits|ci-dessus)|ces (faits|éléments) (ne |montrent|relèvent|portent)`)
	summaryTensionRe = regexp.MustCompile(`(?i)\b(tension|contradiction|contradictoire|paradox|contraste|incohéren|écart entre)`)
	summaryOrderRe   = regexp.MustCompile(`(?i)\b(passé de|est passée? de|d'abord|puis\b|ensuite|désormais|initialement|dans un (premier|second) temps|a évolué)`)
)

// summarySentences counts the sentences of a summary: a terminator followed
// by a space and a capital letter or the end, so that « M. », « n° 3. » and
// « 2 488 » do not count.
func summarySentences(s string) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	n := 0
	runes := []rune(s)
	for i, r := range runes {
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		if i == len(runes)-1 {
			n++
			break
		}
		// « M. Dupont », « J.-L. » : a dot after a one- or two-letter capital
		// abbreviation does not end a sentence.
		if r == '.' {
			j := i - 1
			for j >= 0 && runes[j] != ' ' {
				j--
			}
			word := runes[j+1 : i]
			if len(word) > 0 && len(word) <= 2 && unicode.IsUpper(word[0]) && unicode.IsLetter(word[len(word)-1]) {
				continue
			}
		}
		if runes[i+1] == ' ' || runes[i+1] == '»' {
			j := i + 1
			for j < len(runes) && (runes[j] == ' ' || runes[j] == '»') {
				j++
			}
			if j >= len(runes) || strings.ToUpper(string(runes[j])) == string(runes[j]) && !strings.ContainsRune("0123456789", runes[j]) {
				n++
			}
		}
	}
	if n == 0 {
		n = 1
	}
	return n
}

// TestSummaryGolden is the golden set of the STANCE SUMMARY: the full rebuild
// of a stance's label and summary (StanceManager.refreshStanceMetadata, rule
// body stanceSummaryRules). Each case is a real stance whose summary had a
// problem in an audit; the test checks the shape of the summary the prompt
// writes now, not its words: at most 2 sentences and stanceSummaryMaxRunes
// runes, no comment on the fact list, no tension or contradiction wording,
// no order in time when the facts are undated, and the words of
// must_mention.
//
// LOCAL ONLY: self-skips unless OPEN_AI_KEY is set (see TestAnalyzeGolden).
//
//	source .env; go test -v -run TestSummaryGolden ./backend/core/
func TestSummaryGolden(t *testing.T) {
	llmtest.Setup(t)

	raw, err := os.ReadFile("testdata/summary_cases.yaml")
	if err != nil {
		t.Fatalf("reading summary_cases.yaml: %v", err)
	}
	var cf summaryCaseFile
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("parsing summary_cases.yaml: %v", err)
	}
	if len(cf.Cases) == 0 {
		t.Fatal("no cases in summary_cases.yaml")
	}

	sm, err := NewStanceManagerFromEnv(nil) // storer unused: nothing is written
	if err != nil {
		t.Fatalf("failed to create stance manager: %v", err)
	}

	ctx, cancel := llmtest.Context(t)
	defer cancel()

	var mu sync.Mutex
	var passed, failed int
	llmtest.Group(t, func(t *testing.T) {
		for _, tc := range cf.Cases {
			tc := tc
			t.Run(tc.Name, func(t *testing.T) {
				llmtest.Parallel(t)
				facts := make([]StanceFactInfo, 0, len(tc.Facts))
				for _, f := range tc.Facts {
					facts = append(facts, StanceFactInfo{
						ID:                 f.ID,
						CanonicalVerb:      f.Verb,
						NormalizedFactText: f.Text,
						Lang:               "fr",
						Date:               goldenDate(t, f.Date),
					})
				}

				label, summary, err := sm.refreshStanceMetadata(ctx, tc.StanceID, facts)
				if err != nil {
					t.Fatalf("refreshStanceMetadata: %v", err)
				}
				t.Logf("label=%q\nsummary=%q", label, summary)

				ok := true
				if summary == "" {
					ok = false
					t.Error("empty summary")
				}
				if n := utf8.RuneCountInString(summary); n > stanceSummaryMaxRunes {
					ok = false
					t.Errorf("summary is %d runes, the cap is %d", n, stanceSummaryMaxRunes)
				}
				if n := summarySentences(summary); n > 2 {
					ok = false
					t.Errorf("summary has %d sentences, at most 2", n)
				}
				if m := summaryMetaRe.FindString(summary); m != "" {
					ok = false
					t.Errorf("summary comments on the fact list: %q", m)
				}
				if m := summaryTensionRe.FindString(summary); m != "" {
					ok = false
					t.Errorf("summary names a tension or contradiction: %q", m)
				}
				if tc.NoOrdering {
					if m := summaryOrderRe.FindString(summary); m != "" {
						ok = false
						t.Errorf("summary orders undated facts in time: %q", m)
					}
				}
				lower := strings.ToLower(summary)
				for _, w := range tc.MustMention {
					if !strings.Contains(lower, strings.ToLower(w)) {
						ok = false
						t.Errorf("summary does not mention %q", w)
					}
				}

				mu.Lock()
				defer mu.Unlock()
				if ok {
					passed++
				} else {
					failed++
				}
			})
		}
	})

	fmt.Fprintf(os.Stderr, "\n=== SUMMARY GOLDEN: %d/%d passed (%d failed) ===\n", passed, passed+failed, failed)
}

// TestSummarySentences pins the sentence counter the golden relies on.
func TestSummarySentences(t *testing.T) {
	cases := map[string]int{
		"Il s'oppose à l'aide à mourir. Il veut renforcer les soins palliatifs.": 2,
		"Sur 4 000 commerces, 2 488 sont sinistrés. Il réclame une pause.":       2,
		"Il a voté pour l'article 3. Il soutient la protection de l'enfance.":    2,
		"Elle estime que M. Dupont a tort et demande une loi.":                   1,
		"Une seule phrase sans point final":                                      1,
		"Première phrase. Deuxième phrase ! Troisième phrase ?":                  3,
		"Il défend « la République ». Il propose une constituante.":              2,
	}
	for s, want := range cases {
		if got := summarySentences(s); got != want {
			t.Errorf("%q: %d sentences, want %d", s, got, want)
		}
	}
}
