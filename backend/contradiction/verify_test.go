package contradiction

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// verifyCase is one row of testdata/verify_cases.yaml.
type verifyCase struct {
	Name          string `yaml:"name"`
	FactA         string `yaml:"fact_a"`
	FactB         string `yaml:"fact_b"`
	ClaimedReason string `yaml:"claimed_reason"`
	Expect        string `yaml:"expect"` // "keep" or "drop"
	Bucket        string `yaml:"bucket"`
	Notes         string `yaml:"notes"`
}

type verifyCaseFile struct {
	Cases []verifyCase `yaml:"cases"`
}

// TestVerifyGolden is the manual/golden regression set for the adversarial
// verification (refute) pass. It loads real production cases from
// testdata/verify_cases.yaml and asserts the verifier keeps the true positives
// and drops the false positives.
//
// LOCAL ONLY: it self-skips unless OPEN_AI_KEY is set, so it never runs in CI
// or on the host. The LLM endpoint is the local GPU (OPEN_AI_API), so each run
// is free. Add new cases to the YAML and re-run; tune promptrules/verify.go
// until everything is green.
//
//	source .env; go test -v -run TestVerifyGolden ./backend/contradiction/
func TestVerifyGolden(t *testing.T) {
	_ = godotenv.Overload()
	_ = godotenv.Overload("../../.env") // also try repo-root .env when run from the package dir

	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping golden verify test: OPEN_AI_KEY not set (local-only).")
	}

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

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var passed, failed int
	for _, tc := range cf.Cases {
		tc := tc
		wantKeep, err := expectToKeep(tc.Expect)
		if err != nil {
			t.Fatalf("case %q: %v", tc.Name, err)
		}
		t.Run(tc.Name, func(t *testing.T) {
			keep, reason, err := detector.VerifyContradiction(ctx, tc.FactA, tc.FactB, tc.ClaimedReason)
			if err != nil {
				t.Fatalf("VerifyContradiction error: %v", err)
			}
			got := verdictWord(keep)
			want := verdictWord(wantKeep)
			if keep != wantKeep {
				failed++
				t.Errorf("[%s] got=%s want=%s\n    A: %s\n    B: %s\n    verifier: %s",
					tc.Bucket, got, want, tc.FactA, tc.FactB, reason)
				return
			}
			passed++
			t.Logf("[%s] %s ✓  (%s)", tc.Bucket, got, reason)
		})
	}

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
