package core

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The fact auditor's parse is llm.VerdictJudge.Parse (tested in backend/llm);
// these tests pin the fact-audit enums wired into it: the closed rule list
// and the keep/delete/unsure verdict set.
func newFactAuditorForParse(t *testing.T) *OpenAIFactAuditor {
	t.Helper()
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_API", "http://unused")
	a, err := NewOpenAIFactAuditorFromEnv()
	require.NoError(t, err)
	return a
}

// TestFactAuditRules_ClosedEnum confirms strict membership semantics: only the
// canonical tokens are accepted; anything else is an error so the judge issues
// a corrective retry. LLM-emitted variants ("vague_or_not_concrete",
// "rule_6_vague", "vague (rule 6)") are rejected rather than normalised,
// because tolerant matching hid bugs.
func TestFactAuditRules_ClosedEnum(t *testing.T) {
	a := newFactAuditorForParse(t)
	good := []string{
		"none", "vague", "speculative", "pattern_of_behavior",
		"not_about_entity", "lawyers_as_subject", "not_contradictable",
		"journalist_characterization", "polls_or_surveys",
		"  Vague  ", "VAGUE", // case + whitespace tolerated
	}
	for _, raw := range good {
		verdict := "delete"
		if strings.TrimSpace(strings.ToLower(raw)) == "none" {
			verdict = "keep"
		}
		res, err := a.judge.Parse("REASONING: x.\nRULE: " + raw + "\nVERDICT: " + verdict)
		require.NoErrorf(t, err, "expected %q to validate", raw)
		assert.Equalf(t, strings.ToLower(strings.TrimSpace(raw)), res.Rule, "input=%q", raw)
	}

	bad := []string{
		"",
		"vague_or_not_concrete",
		"rule_6_vague_or_not_concrete",
		"vague (rule 6)",
		"vague or not concrete",
		"pattern-of-behavior / meta-observation about the entity",
		"not_about_the_entity_or_their_organization",
		"banana",
	}
	for _, raw := range bad {
		_, err := a.judge.Parse("REASONING: x.\nRULE: " + raw + "\nVERDICT: delete")
		require.Errorf(t, err, "expected %q to be rejected", raw)
	}
}

func TestFactAuditParse_Verdicts(t *testing.T) {
	a := newFactAuditorForParse(t)
	res, err := a.judge.Parse("REASONING: prediction about a third party.\nRULE: speculative\nVERDICT: delete")
	require.NoError(t, err)
	assert.Equal(t, string(FactVerdictDelete), res.Verdict)
	assert.Equal(t, "speculative", res.Rule)

	res, err = a.judge.Parse("REASONING: borderline.\nRULE: vague\nVERDICT: unsure")
	require.NoError(t, err)
	assert.Equal(t, string(FactVerdictUnsure), res.Verdict)

	_, err = a.judge.Parse("REASONING: solid.\nRULE: vague\nVERDICT: keep")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VERDICT=keep requires RULE=none")

	// A verdict from the contradiction auditor's enum is not a verdict here.
	_, err = a.judge.Parse("REASONING: x.\nRULE: vague\nVERDICT: remove")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not parse VERDICT line")
}
