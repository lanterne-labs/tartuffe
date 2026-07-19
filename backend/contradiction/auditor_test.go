package contradiction

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseAuditResponse_Keep(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: Both statements directly contradict on the person's own knowledge of the same specific event.",
		"RULE: none",
		"VERDICT: keep",
	}, "\n")
	res := parseAuditResponse(resp)
	require.NoError(t, res.Err)
	assert.Equal(t, VerdictKeep, res.Verdict)
	assert.Equal(t, "none", res.Rule)
	assert.Contains(t, res.Reasoning, "directly contradict")
}

func TestParseAuditResponse_Remove(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: Statement A is an indictment, statement B is a denial; an indictment is not an authoritative finding.",
		"RULE: accusation_not_finding",
		"VERDICT: remove",
	}, "\n")
	res := parseAuditResponse(resp)
	require.NoError(t, res.Err)
	assert.Equal(t, VerdictRemove, res.Verdict)
	assert.Equal(t, "accusation_not_finding", res.Rule)
}

func TestParseAuditResponse_Ambiguous(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: Both dates unknown; cannot determine temporal ordering.",
		"RULE: different_events",
		"VERDICT: ambiguous",
	}, "\n")
	res := parseAuditResponse(resp)
	require.NoError(t, res.Err)
	assert.Equal(t, VerdictAmbiguous, res.Verdict)
}

func TestParseAuditResponse_CaseInsensitiveAndExtraWhitespace(t *testing.T) {
	resp := strings.Join([]string{
		"   reasoning :  Different geographic scope.",
		"  Rule:  geographic_scope",
		"  verdict :  REMOVE  with explanation",
	}, "\n")
	res := parseAuditResponse(resp)
	require.NoError(t, res.Err)
	assert.Equal(t, VerdictRemove, res.Verdict)
	assert.Equal(t, "geographic_scope", res.Rule)
}

func TestParseAuditResponse_NoiseBeforeAndAfter(t *testing.T) {
	resp := strings.Join([]string{
		"Sure, here's my analysis:",
		"",
		"REASONING: Paraphrase of the same position.",
		"RULE: paraphrase_same_position",
		"VERDICT: remove",
		"",
		"Let me know if you need more detail.",
	}, "\n")
	res := parseAuditResponse(resp)
	require.NoError(t, res.Err)
	assert.Equal(t, VerdictRemove, res.Verdict)
	assert.Contains(t, res.Reasoning, "Paraphrase")
}

func TestParseAuditResponse_MissingVerdictReturnsError(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: I think this might be a contradiction but I'm not certain.",
		"RULE: none",
	}, "\n")
	res := parseAuditResponse(resp)
	assert.Error(t, res.Err)
	assert.Equal(t, AuditVerdict(""), res.Verdict)
}

// Strict-mode contract: a RULE token outside the closed enum is rejected with
// an error so the auditor can issue a corrective retry. We deliberately do NOT
// fuzzy-match variants — silent normalisation hid extraction bugs in earlier
// runs and produced unstable rule frequencies in metrics.
func TestParseAuditResponse_RejectsUnknownRule(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: not in our enum.",
		"RULE: vague_or_not_concrete (rule 6)",
		"VERDICT: remove",
	}, "\n")
	res := parseAuditResponse(resp)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "closed enum")
	// Verdict is still parsed; rule surfaces the bad raw value so the retry
	// prompt can quote it.
	assert.Equal(t, VerdictRemove, res.Verdict)
}

// Trailing prose on the RULE line ("geographic_scope (the fact about ...)") is
// also rejected — the prompt asks for EXACTLY one token.
func TestParseAuditResponse_RejectsRuleWithTrailingProse(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: different geographic scope.",
		"RULE: geographic_scope (the fact about Vienne is not national)",
		"VERDICT: remove",
	}, "\n")
	res := parseAuditResponse(resp)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "closed enum")
}

// Cross-check: keep requires none; otherwise it's a contradiction we can't
// resolve and the caller should retry.
func TestParseAuditResponse_KeepRequiresNone(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: actually contradictory.",
		"RULE: learning_over_time",
		"VERDICT: keep",
	}, "\n")
	res := parseAuditResponse(resp)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "VERDICT=keep requires RULE=none")
}

func TestParseAuditResponse_OnlyFirstVerdictWins(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: First reasoning.",
		"RULE: none",
		"VERDICT: keep",
		"REASONING: Second reasoning.",
		"VERDICT: remove",
	}, "\n")
	res := parseAuditResponse(resp)
	require.NoError(t, res.Err)
	assert.Equal(t, VerdictKeep, res.Verdict)
	assert.Equal(t, "First reasoning.", res.Reasoning)
	assert.Equal(t, "none", res.Rule)
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
