package core

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// analyzeKeyFact is one stance member in testdata/analyze_cases.yaml.
type analyzeKeyFact struct {
	ID   int64  `yaml:"id"`
	Verb string `yaml:"verb"`
	Text string `yaml:"text"`
}

// analyzeCase is one row of testdata/analyze_cases.yaml.
type analyzeCase struct {
	Name          string           `yaml:"name"`
	Summary       string           `yaml:"summary"`
	KeyFacts      []analyzeKeyFact `yaml:"key_facts"`
	NewFact       string           `yaml:"new_fact"`
	ExpectVerdict string           `yaml:"expect_verdict"` // optional; empty = don't assert
	MustFlag      []int64          `yaml:"must_flag"`
	MustNotFlag   []int64          `yaml:"must_not_flag"`
	Notes         string           `yaml:"notes"`
}

type analyzeCaseFile struct {
	Cases []analyzeCase `yaml:"cases"`
}

// TestAnalyzeGolden is the golden regression set for the STANCE ANALYZE judge
// (StanceManager.callLLM / analyzeAcrossBatches — rule body:
// promptrules.ContradictionRules). This is the judge that CREATES contradiction
// edges at ingest; the verifier and auditor have their own golden sets under
// backend/contradiction/testdata/.
//
// Assertions are edge-level: every ID in must_flag has to appear in the
// returned contradictions, every ID in must_not_flag must not. The primary
// verdict is asserted only when expect_verdict is set.
//
// LOCAL ONLY: self-skips unless OPEN_AI_KEY is set, so it never runs in CI or
// on the host. The LLM endpoint is the local GPU (OPEN_AI_API), so each run is
// free. Add new cases to the YAML and re-run; tune promptrules/contradiction.go
// (consumed ONLY by this judge) until everything is green.
//
//	source .env; go test -v -run TestAnalyzeGolden ./backend/core/
func TestAnalyzeGolden(t *testing.T) {
	_ = godotenv.Overload()
	_ = godotenv.Overload("../../.env") // repo-root .env when run from the package dir

	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping golden analyze test: OPEN_AI_KEY not set (local-only).")
	}

	raw, err := os.ReadFile("testdata/analyze_cases.yaml")
	if err != nil {
		t.Fatalf("reading analyze_cases.yaml: %v", err)
	}
	var cf analyzeCaseFile
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("parsing analyze_cases.yaml: %v", err)
	}
	if len(cf.Cases) == 0 {
		t.Fatal("no cases in analyze_cases.yaml")
	}

	sm, err := NewStanceManagerFromEnv(nil) // storer unused: we never record
	if err != nil {
		t.Fatalf("failed to create stance manager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()

	var passed, failed int
	for _, tc := range cf.Cases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			keyFacts := make([]StanceFactInfo, 0, len(tc.KeyFacts))
			for _, kf := range tc.KeyFacts {
				keyFacts = append(keyFacts, StanceFactInfo{
					ID:                 kf.ID,
					CanonicalVerb:      kf.Verb,
					NormalizedFactText: kf.Text,
				})
			}

			agg, err := sm.analyzeAcrossBatches(ctx, tc.Summary, keyFacts, tc.NewFact)
			if err != nil {
				t.Fatalf("analyzeAcrossBatches: %v", err)
			}

			flagged := make(map[int64]string, len(agg.Contradictions))
			for _, c := range agg.Contradictions {
				flagged[c.KeyFactID] = c.Reason
			}

			ok := true
			if tc.ExpectVerdict != "" && agg.PrimaryVerdict != tc.ExpectVerdict {
				ok = false
				t.Errorf("verdict: got=%s want=%s (reasoning: %s)", agg.PrimaryVerdict, tc.ExpectVerdict, agg.Reasoning)
			}
			for _, id := range tc.MustFlag {
				if _, hit := flagged[id]; !hit {
					ok = false
					t.Errorf("MISSING edge: key fact %d not flagged (verdict=%s, flagged=%v)", id, agg.PrimaryVerdict, keysOf(flagged))
				}
			}
			for _, id := range tc.MustNotFlag {
				if reason, hit := flagged[id]; hit {
					ok = false
					t.Errorf("OVERFIRE edge: key fact %d flagged (reason: %s)", id, reason)
				}
			}

			if ok {
				passed++
				t.Logf("verdict=%s flagged=%v ✓", agg.PrimaryVerdict, keysOf(flagged))
			} else {
				failed++
			}
		})
	}

	fmt.Fprintf(os.Stderr, "\n=== ANALYZE GOLDEN: %d/%d passed (%d failed) ===\n", passed, passed+failed, failed)
}

// keysOf returns the flagged key-fact IDs for compact log/error output.
func keysOf(m map[int64]string) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
