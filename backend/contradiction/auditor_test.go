package contradiction

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The contradiction auditor's parse is llm.VerdictJudge.Parse (tested in
// backend/llm); these tests pin the enums wired into it: every catalog rule
// token is accepted and the verdict set is keep/remove/ambiguous.
func newAuditorForParse(t *testing.T) *OpenAIContradictionAuditor {
	t.Helper()
	t.Setenv("OPEN_AI_KEY", "k")
	t.Setenv("OPEN_AI_API", "http://unused")
	a, err := NewOpenAIContradictionAuditorFromEnv()
	require.NoError(t, err)
	return a
}

func TestAuditParse_EveryRuleTokenAccepted(t *testing.T) {
	a := newAuditorForParse(t)
	for _, rule := range append(append([]string{}, contradictionAuditAllowedRulesList...), contradictionAuditUnlistedRules...) {
		verdict := "remove"
		if rule == "none" {
			verdict = "keep"
		}
		res, err := a.judge.Parse("REASONING: x.\nRULE: " + rule + "\nVERDICT: " + verdict)
		require.NoErrorf(t, err, "rule %q", rule)
		assert.Equal(t, rule, res.Rule)
	}
}

func TestAuditParse_Verdicts(t *testing.T) {
	a := newAuditorForParse(t)
	res, err := a.judge.Parse("REASONING: indictment vs denial.\nRULE: accusation_not_finding\nVERDICT: remove")
	require.NoError(t, err)
	assert.Equal(t, string(VerdictRemove), res.Verdict)

	res, err = a.judge.Parse("REASONING: dates unknown.\nRULE: different_events\nVERDICT: ambiguous")
	require.NoError(t, err)
	assert.Equal(t, string(VerdictAmbiguous), res.Verdict)

	res, err = a.judge.Parse("REASONING: real reversal.\nRULE: none\nVERDICT: keep")
	require.NoError(t, err)
	assert.Equal(t, string(VerdictKeep), res.Verdict)

	_, err = a.judge.Parse("REASONING: x.\nRULE: vague_or_not_concrete (rule 6)\nVERDICT: remove")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed enum")

	// A verdict from the fact auditor's enum is not a verdict here.
	_, err = a.judge.Parse("REASONING: x.\nRULE: vague_vs_specific\nVERDICT: delete")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not parse VERDICT line")
}

// TestLeanAuditRules locks the auditor's curated-example view of the shared
// rule body: every allowlisted example present, the cut ones absent, the rule
// sections (incl. the FP catalog) intact, and the detector's full body
// untouched.
func TestLeanAuditRules(t *testing.T) {
	lean := leanAuditRules()
	for _, want := range auditorKeptExamples {
		if !strings.Contains(lean, "\n"+want) && !strings.Contains(lean, "\n\n"+want) && !strings.Contains(lean, want) {
			t.Errorf("lean body missing kept example %q", want)
		}
	}
	for _, cut := range []string{"PAIR 4 (", "PAIR 10 (", "PAIR 21 (", "PAIR 32 ("} {
		if strings.Contains(lean, cut) {
			t.Errorf("lean body still contains cut example %q", cut)
		}
	}
	for _, section := range []string{"HARD GATE", "false-positive patterns", "TEMPORAL REASONING", "**EXAMPLES:**"} {
		if !strings.Contains(lean, section) {
			t.Errorf("lean body lost rule section %q", section)
		}
	}
	if !strings.Contains(ContradictionRulesAndExamples, "PAIR 32 (") {
		t.Error("detector full body unexpectedly modified")
	}
	if len(lean) >= len(ContradictionRulesAndExamples) {
		t.Errorf("lean body (%d chars) not smaller than full body (%d)", len(lean), len(ContradictionRulesAndExamples))
	}
}
