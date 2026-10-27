package contradiction

import (
	"digital-mirror/backend/llmtest"
	"digital-mirror/backend/promptrules"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// verifyCase is one row of testdata/verify_cases.yaml.
type verifyCase struct {
	Name  string `yaml:"name"`
	FactA string `yaml:"fact_a"`
	FactB string `yaml:"fact_b"`
	// AlsoA / AlsoB are other wordings of a statement that stands for a group
	// of restatements (promptrules.Statement.Also).
	AlsoA []string `yaml:"also_a"`
	AlsoB []string `yaml:"also_b"`
	// DateA / DateB are the days of the statements (YYYY-MM-DD), optional.
	DateA         string `yaml:"date_a"`
	DateB         string `yaml:"date_b"`
	ClaimedReason string `yaml:"claimed_reason"`
	Expect        string `yaml:"expect"` // "keep" or "drop"
	Bucket        string `yaml:"bucket"`
	Notes         string `yaml:"notes"`
	// Judges scopes the case to specific golden suites: "verify" and/or
	// "audit". Empty = both (the default for every pre-existing case). Use it
	// when only ONE judge handles the shape reliably — the other judge's gap
	// stays documented in a comment instead of a flaky assert.
	Judges []string `yaml:"judges"`
}

// judgedBy reports whether the case participates in the given suite
// ("verify" or "audit"). An empty Judges list means all suites.
func (c verifyCase) judgedBy(judge string) bool {
	if len(c.Judges) == 0 {
		return true
	}
	for _, j := range c.Judges {
		if j == judge {
			return true
		}
	}
	return false
}

type verifyCaseFile struct {
	Cases []verifyCase `yaml:"cases"`
}

// TestVerifyGolden is the manual/golden regression set for the adversarial
// verification (refute) pass — the INSERTION gate that runs on every
// contradiction the stance analyzer proposes, before the DB write. It loads
// real production cases from testdata/verify_cases.yaml and asserts the
// verifier keeps the true positives and drops the false positives.
// The same YAML also feeds TestAuditGolden (a different judge, different
// prompt); the analyze judge has its own set in backend/core/testdata/.
//
// LOCAL ONLY: it self-skips unless OPEN_AI_KEY is set, so it never runs in CI
// or on the host. The LLM endpoint is the local GPU (OPEN_AI_API), so each run
// is free. Add new cases to the YAML and re-run; tune promptrules/verify.go
// until everything is green.
//
//	source .env; go test -v -run TestVerifyGolden ./backend/contradiction/
func TestVerifyGolden(t *testing.T) {
	llmtest.Setup(t)

	raw, err := os.ReadFile("testdata/verify_cases.yaml")
	if err != nil {
		t.Fatalf("reading verify_cases.yaml: %v", err)
	}
	var cf verifyCaseFile
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("parsing verify_cases.yaml: %v", err)
	}
	if len(cf.Cases) == 0 {
		t.Fatal("no cases in verify_cases.yaml")
	}

	detector, err := NewOpenAIContradictionDetectorFromEnv()
	if err != nil {
		t.Fatalf("failed to create detector: %v", err)
	}

	ctx, cancel := llmtest.Context(t)
	defer cancel()

	// The cases run concurrently with -llm-claude: mu guards the counts.
	var mu sync.Mutex
	var passed, failed int
	llmtest.Group(t, func(t *testing.T) {
		for _, tc := range cf.Cases {
			tc := tc
			if !tc.judgedBy("verify") {
				continue
			}
			wantKeep, err := expectToKeep(tc.Expect)
			if err != nil {
				t.Fatalf("case %q: %v", tc.Name, err)
			}
			t.Run(tc.Name, func(t *testing.T) {
				llmtest.Parallel(t)
				keep, reason, err := detector.VerifyContradiction(ctx, promptrules.Statement{Text: tc.FactA, Also: tc.AlsoA, Date: tc.DateA}, promptrules.Statement{Text: tc.FactB, Also: tc.AlsoB, Date: tc.DateB}, tc.ClaimedReason)
				if err != nil {
					t.Fatalf("VerifyContradiction error: %v", err)
				}
				got := verdictWord(keep)
				want := verdictWord(wantKeep)
				if keep != wantKeep {
					mu.Lock()
					failed++
					mu.Unlock()
					t.Errorf("[%s] got=%s want=%s\n    A: %s\n    B: %s\n    verifier: %s",
						tc.Bucket, got, want, tc.FactA, tc.FactB, reason)
					return
				}
				mu.Lock()
				passed++
				mu.Unlock()
				t.Logf("[%s] %s ✓  (%s)", tc.Bucket, got, reason)
			})
		}
	})

	// A compact scoreboard so it's obvious at a glance how the set is doing.
	fmt.Fprintf(os.Stderr, "\n=== VERIFY GOLDEN: %d/%d passed (%d failed) ===\n", passed, passed+failed, failed)
}

func expectToKeep(s string) (bool, error) {
	switch s {
	case "keep":
		return true, nil
	case "drop":
		return false, nil
	default:
		return false, fmt.Errorf("invalid expect %q (want keep|drop)", s)
	}
}

func verdictWord(keep bool) string {
	if keep {
		return "keep"
	}
	return "drop"
}

// caseDate parses the optional YYYY-MM-DD of a case; empty is nil (undated),
// as the audit path passes a row's date.
func caseDate(t *testing.T, s string) *time.Time {
	t.Helper()
	if s == "" {
		return nil
	}
	d, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("case date %q: %v", s, err)
	}
	return &d
}
