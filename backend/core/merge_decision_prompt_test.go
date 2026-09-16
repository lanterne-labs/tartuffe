package core

import (
	"strings"
	"testing"
)

// TestMergeDecisionPromptSharesRulePrefix pins the prefix-cache layout of the
// merge-decision prompt: two calls with different persons and clusters share
// every byte up to the first per-call datum ("PERSON:"), the person never
// appears before it, and the JSON response format comes after the stances.
func TestMergeDecisionPromptSharesRulePrefix(t *testing.T) {
	p1 := mergeDecisionPrompt("Marine Le Pen", []StanceForMerge{
		{ID: 1, Label: "réforme des retraites", Summary: "a"},
		{ID: 2, Label: "âge légal de départ", Summary: "b"},
	})
	p2 := mergeDecisionPrompt("Gérald Darmanin", []StanceForMerge{
		{ID: 7, Label: "politique de sécurité intérieure", Summary: "c"},
		{ID: 8, Label: "politique pénale", Summary: "d"},
		{ID: 9, Label: "immigration", Summary: "e"},
	})

	marker := strings.Index(p1, "\nPERSON: ")
	if marker < 0 {
		t.Fatalf("no PERSON: data marker in prompt:\n%s", p1)
	}
	shared := 0
	for shared < len(p1) && shared < len(p2) && p1[shared] == p2[shared] {
		shared++
	}
	if shared < marker {
		t.Errorf("shared prefix is %d bytes, diverges before the data marker at %d:\n%s", shared, marker, p1[shared:min(len(p1), shared+80)])
	}
	if marker < 5000 {
		t.Errorf("cacheable rule prefix is only %d bytes; the merge rulebook should precede the data", marker)
	}
	if strings.Contains(p1[:marker], "Marine Le Pen") {
		t.Errorf("person appears before the data marker")
	}
	format := strings.Index(p1, "Return ONLY valid JSON")
	if stances := strings.Index(p1, "STANCES IN THIS CANDIDATE CLUSTER:"); format < 0 || format < stances {
		t.Errorf("response format must come after the stances (format at %d, stances at %d)", format, stances)
	}
	if !strings.Contains(p1, "[2] id=2, label=\"âge légal de départ\"") {
		t.Errorf("stance listing missing from prompt")
	}
}
