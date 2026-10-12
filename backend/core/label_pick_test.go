package core

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"digital-mirror/backend/llmtest"

	"gopkg.in/yaml.v3"
)

type labelPickCase struct {
	Name        string   `yaml:"name"`
	NewFact     string   `yaml:"new_fact"`
	Labels      []string `yaml:"labels"`
	ExpectAnyOf []string `yaml:"expect_any_of"` // empty = the pass must pick nothing
}

// TestLabelPickGolden is the golden set of the topic label pass (recall stage
// of topic assignment): with only a person's labels in view, a fitting topic
// must be among the picks, and nothing may be picked when no topic fits.
// LOCAL ONLY: self-skips unless OPEN_AI_KEY is set (the LLM is the local GPU).
func TestLabelPickGolden(t *testing.T) {
	llmtest.Setup(t)
	raw, err := os.ReadFile("testdata/label_pick_cases.yaml")
	if err != nil {
		t.Fatalf("reading label_pick_cases.yaml: %v", err)
	}
	var cf struct {
		Cases []labelPickCase `yaml:"cases"`
	}
	if err := yaml.Unmarshal(raw, &cf); err != nil {
		t.Fatalf("parsing label_pick_cases.yaml: %v", err)
	}
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
				stances := make([]StanceDocument, len(tc.Labels))
				byID := make(map[int64]string, len(tc.Labels))
				for i, l := range tc.Labels {
					stances[i] = StanceDocument{ID: int64(i + 1), ClusterLabel: l}
					byID[int64(i+1)] = l
				}
				ids, err := sm.pickTopicsByLabel(ctx, tc.NewFact, stances)
				if err != nil {
					t.Fatalf("pickTopicsByLabel error: %v", err)
				}
				mu.Lock()
				defer mu.Unlock()
				picked := make([]string, 0, len(ids))
				for _, id := range ids {
					picked = append(picked, byID[id])
				}
				if len(tc.ExpectAnyOf) == 0 {
					if len(picked) > 0 {
						failed++
						t.Errorf("got=%q want=nothing\n    fact: %s", picked, tc.NewFact)
						return
					}
					passed++
					return
				}
				for _, p := range picked {
					for _, e := range tc.ExpectAnyOf {
						if p == e {
							passed++
							t.Logf("picked %q ✓", picked)
							return
						}
					}
				}
				failed++
				t.Errorf("got=%q want one of %q\n    fact: %s", picked, tc.ExpectAnyOf, tc.NewFact)
			})
		}
	})
	fmt.Fprintf(os.Stderr, "\n=== LABEL PICK GOLDEN: %d/%d passed (%d failed) ===\n", passed, passed+failed, failed)
}
