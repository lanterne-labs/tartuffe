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

// assignCandidate is one candidate stance in a golden assignment case.
type assignCandidate struct {
	Label   string   `yaml:"label"`
	Summary string   `yaml:"summary"`
	Facts   []string `yaml:"facts"`
}

// assignCase is one row of testdata/assign_cases.yaml.
type assignCase struct {
	Name       string            `yaml:"name"`
	NewFact    string            `yaml:"new_fact"`
	Candidates []assignCandidate `yaml:"candidates"`
	Expect     string            `yaml:"expect"` // exact candidate label, or "NEW"
}

type assignCaseFile struct {
	Cases []assignCase `yaml:"cases"`
}

// TestAssignGolden is the manual/golden regression set for topic assignment
// routing (assignTopicFromCandidates). It loads fixture cases from
// testdata/assign_cases.yaml and asserts each new fact routes to the expected
// single-subject candidate (or to a NEW topic), guarding against the mega-stance
// attractor where loosely related facts got vacuumed into broad multi-subject
// buckets.
//
// LOCAL ONLY: it self-skips unless OPEN_AI_KEY is set, so it never runs in CI or
// on the host. The LLM endpoint is the local GPU (OPEN_AI_API), so each run is
// free. Add new cases to the YAML and re-run; tune the prompt in
// assignTopicFromCandidates until everything is green.
//
//	source .env; go test -v -run TestAssignGolden ./backend/core/
func TestAssignGolden(t *testing.T) {
	_ = godotenv.Overload()
	_ = godotenv.Overload("../../.env") // also try repo-root .env when run from the package dir

	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("Skipping golden assign test: OPEN_AI_KEY not set (local-only).")
	}

	raw, err := os.ReadFile("testdata/assign_cases.yaml")
	if err != nil {
		t.Fatalf("reading assign_cases.yaml: %v", err)
	}
	var cf assignCaseFile
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("parsing assign_cases.yaml: %v", err)
	}
	if len(cf.Cases) == 0 {
		t.Fatal("no cases in assign_cases.yaml")
	}

	// assignTopicFromCandidates is pure (LLM only, no storer), so a nil storer is
	// fine here — NewStanceManagerFromEnv only reads OPEN_AI_* env vars.
	sm, err := NewStanceManagerFromEnv(nil)
	if err != nil {
		t.Fatalf("failed to create stance manager: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var passed, failed int
	for _, tc := range cf.Cases {
		tc := tc
		t.Run(tc.Name, func(t *testing.T) {
			// Synthetic IDs 1..N by order so we can map the decision back to a label.
			candidates := make([]StanceCandidate, len(tc.Candidates))
			labelByID := make(map[int64]string, len(tc.Candidates))
			for i, c := range tc.Candidates {
				id := int64(i + 1)
				candidates[i] = StanceCandidate{
					ID:          id,
					Label:       c.Label,
					Summary:     c.Summary,
					FactCount:   len(c.Facts),
					MemberFacts: c.Facts,
				}
				labelByID[id] = c.Label
			}

			decision, err := sm.assignTopicFromCandidates(ctx, tc.NewFact, candidates)
			if err != nil {
				t.Fatalf("assignTopicFromCandidates error: %v", err)
			}

			if tc.Expect == "NEW" {
				if !decision.IsNew {
					failed++
					t.Errorf("got=ASSIGNED(%q) want=NEW\n    fact: %s\n    reasoning: %s",
						labelByID[decision.AssignedID], tc.NewFact, decision.Reasoning)
					return
				}
				passed++
				t.Logf("NEW ✓ label=%q (%s)", decision.NewLabel, decision.Reasoning)
				return
			}

			if decision.IsNew {
				failed++
				t.Errorf("got=NEW(%q) want=%q\n    fact: %s\n    reasoning: %s",
					decision.NewLabel, tc.Expect, tc.NewFact, decision.Reasoning)
				return
			}
			got := labelByID[decision.AssignedID]
			if got != tc.Expect {
				failed++
				t.Errorf("got=%q want=%q\n    fact: %s\n    reasoning: %s",
					got, tc.Expect, tc.NewFact, decision.Reasoning)
				return
			}
			passed++
			t.Logf("ASSIGNED %q ✓  (%s)", got, decision.Reasoning)
		})
	}

	// Compact scoreboard so the set's health is obvious at a glance.
	fmt.Fprintf(os.Stderr, "\n=== ASSIGN GOLDEN: %d/%d passed (%d failed) ===\n", passed, passed+failed, failed)
}
