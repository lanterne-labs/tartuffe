package categorizer

import (
	"os"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"

	"digital-mirror/backend"
	"digital-mirror/backend/llmtest"
)

// groundingCase is one row of testdata/grounding_cases.yaml.
type groundingCase struct {
	Name    string   `yaml:"name"`
	Subject string   `yaml:"subject"`
	Fact    string   `yaml:"fact"`
	Quote   string   `yaml:"quote"`
	Context string   `yaml:"context"`
	Expect  string   `yaml:"expect"` // grounded, fix (rewrite or drop), drop, grounded_or_fix
	Forbid  []string `yaml:"forbid"` // phrases a rewrite must not keep, unless its quote holds them
	// QuoteForbid are words the final quote must not hold: another
	// speaker's words a longer quote must not take in.
	QuoteForbid []string `yaml:"quote_forbid"`
}

// TestGroundingGolden runs the grounding check on the facts of the audit of
// 2026-11-01: the glosses must be cut down to their quote or dropped, the
// clean facts kept as they are.
func TestGroundingGolden(t *testing.T) {
	llmtest.Setup(t)
	raw, err := os.ReadFile("testdata/grounding_cases.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []groundingCase `yaml:"cases"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	ext, err := NewOpenAIExtractorFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := llmtest.Context(t)
	defer cancel()

	var mu sync.Mutex
	passed := 0
	llmtest.Group(t, func(t *testing.T) {
		for _, tc := range file.Cases {
			t.Run(tc.Name, func(t *testing.T) {
				llmtest.Parallel(t)
				in := backend.Fact{SubjectEntity: tc.Subject, NormalizedFactText: tc.Fact, OriginalQuote: tc.Quote, Lang: "fr"}
				out, res, err := groundFacts(ctx, ext.client, tc.Subject, tc.Context, "golden://"+tc.Name, []backend.Fact{in})
				if err != nil {
					t.Fatalf("grounding: %v", err)
				}
				if res.Unchecked > 0 {
					t.Fatalf("the fact was left unchecked")
				}
				got := "grounded"
				switch {
				case res.Dropped == 1:
					got = "drop"
				case res.Rewritten == 1:
					got = "rewrite"
				}
				ok := false
				switch tc.Expect {
				case "grounded":
					ok = got == "grounded"
				case "drop":
					ok = got == "drop"
				case "fix", "grounded_or_fix":
					ok = got == "drop" || (tc.Expect == "grounded_or_fix" && got == "grounded")
					if got == "rewrite" {
						ok = true
						f := out[0]
						for _, p := range tc.Forbid {
							if containsWord(f.NormalizedFactText, p) && !containsWord(f.OriginalQuote, p) {
								ok = false
								t.Logf("the rewrite keeps %q", p)
							}
						}
						for _, p := range tc.QuoteForbid {
							if containsWord(f.OriginalQuote, p) {
								ok = false
								t.Logf("the quote took in %q", p)
							}
						}
					}
				}
				detail := ""
				if len(out) == 1 && got == "rewrite" {
					detail = " → " + out[0].NormalizedFactText + " / quote: " + out[0].OriginalQuote
				}
				if !ok {
					t.Errorf("expect %s, got %s%s", tc.Expect, got, detail)
					return
				}
				t.Logf("%s%s", got, detail)
				mu.Lock()
				passed++
				mu.Unlock()
			})
		}
	})
	t.Logf("grounding golden: %d/%d", passed, len(file.Cases))
}

// containsWord reports whether s holds p, case-folded; a short all-caps
// token (RN, PS) must stand as a word.
func containsWord(s, p string) bool {
	if len(p) <= 3 && strings.ToUpper(p) == p {
		for _, w := range strings.FieldsFunc(s, func(r rune) bool { return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z') }) {
			if w == p {
				return true
			}
		}
		return false
	}
	return strings.Contains(strings.ToLower(s), strings.ToLower(p))
}
