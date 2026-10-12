package core

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"digital-mirror/backend/llmtest"

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
	// ExpectAny lists every acceptable outcome (labels or "NEW") for a case
	// whose taxonomy is genuinely debatable; used instead of Expect so the
	// golden documents the ambiguity without asserting a coin flip.
	ExpectAny []string `yaml:"expect_any"`
}

// accepts reports whether the outcome (a candidate label, or "NEW") satisfies
// the case.
func (c assignCase) accepts(outcome string) bool {
	if len(c.ExpectAny) > 0 {
		for _, e := range c.ExpectAny {
			if e == outcome {
				return true
			}
		}
		return false
	}
	return outcome == c.Expect
}

func (c assignCase) want() string {
	if len(c.ExpectAny) > 0 {
		return "one of " + fmt.Sprint(c.ExpectAny)
	}
	return c.Expect
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
	llmtest.Setup(t)

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

	ctx, cancel := llmtest.Context(t)
	defer cancel()

	// The cases run concurrently with -llm-claude: mu guards the counts.
	var mu sync.Mutex
	var passed, failed int
	llmtest.Group(t, func(t *testing.T) {
		for _, tc := range cf.Cases {
			tc := tc
			t.Run(tc.Name, func(t *testing.T) {
				llmtest.Parallel(t)
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
				mu.Lock()
				defer mu.Unlock()

				outcome := "NEW"
				if !decision.IsNew {
					outcome = labelByID[decision.AssignedID]
				}
				if !tc.accepts(outcome) {
					failed++
					got := outcome
					if decision.IsNew {
						got = fmt.Sprintf("NEW(%q)", decision.NewLabel)
					}
					t.Errorf("got=%s want=%s\n    fact: %s\n    reasoning: %s", got, tc.want(), tc.NewFact, decision.Reasoning)
					return
				}
				if decision.IsNew {
					passed++
					t.Logf("NEW ✓ label=%q (%s)", decision.NewLabel, decision.Reasoning)
					return
				}
				passed++
				t.Logf("ASSIGNED %q ✓  (%s)", outcome, decision.Reasoning)
			})
		}
	})

	// Compact scoreboard so the set's health is obvious at a glance.
	fmt.Fprintf(os.Stderr, "\n=== ASSIGN GOLDEN: %d/%d passed (%d failed) ===\n", passed, passed+failed, failed)
}
