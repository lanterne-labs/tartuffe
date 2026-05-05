package core

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestValidateFactAuditRule_ClosedEnum confirms strict membership semantics:
// only the canonical tokens are accepted; anything else returns an error so
// the auditor can issue a corrective retry. We deliberately reject the
// LLM-emitted variants ("vague_or_not_concrete", "rule_6_vague", "vague (rule
// 6)") rather than normalising them, because tolerant matching hid bugs.
func TestValidateFactAuditRule_ClosedEnum(t *testing.T) {
	good := []string{
		"none", "vague", "speculative", "pattern_of_behavior",
		"not_about_entity", "lawyers_as_subject", "not_contradictable",
		"journalist_characterization", "polls_or_surveys",
		"  None  ", "VAGUE", // case + whitespace tolerated
	}
	for _, raw := range good {
		got, err := validateFactAuditRule(raw)
		require.NoErrorf(t, err, "expected %q to validate", raw)
		assert.Equalf(t, strings.ToLower(strings.TrimSpace(raw)), got, "input=%q", raw)
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
		_, err := validateFactAuditRule(raw)
		require.Errorf(t, err, "expected %q to be rejected", raw)
	}
}

// TestParseFactAuditResponse_RejectsBadRule covers the end-to-end parser flow:
// a RULE outside the closed enum surfaces as an error so Audit() can retry.
func TestParseFactAuditResponse_RejectsBadRule(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: vague qualifiers, no concrete metric.",
		"RULE: vague_or_not_concrete (rule 6)",
		"VERDICT: delete",
	}, "\n")
	res := parseFactAuditResponse(resp)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "closed enum")
	assert.Equal(t, FactVerdictDelete, res.Verdict)
}

// Cross-check: keep verdicts must pair with rule "none". Anything else is a
// model self-contradiction worth retrying.
func TestParseFactAuditResponse_KeepRequiresNone(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: solid Type 1 fact.",
		"RULE: vague",
		"VERDICT: keep",
	}, "\n")
	res := parseFactAuditResponse(resp)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "VERDICT=keep requires RULE=none")
}

// And the inverse: delete/unsure verdicts must NOT pair with rule "none" —
// the model has to name a rule.
func TestParseFactAuditResponse_DeleteRequiresRule(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: I think this is bad but cannot say why.",
		"RULE: none",
		"VERDICT: delete",
	}, "\n")
	res := parseFactAuditResponse(resp)
	require.Error(t, res.Err)
	assert.Contains(t, res.Err.Error(), "non-'none'")
}

// Happy path: a clean canonical response parses without error.
func TestParseFactAuditResponse_HappyPath(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: prediction about a third party, not the subject's own action.",
		"RULE: speculative",
		"VERDICT: delete",
	}, "\n")
	res := parseFactAuditResponse(resp)
	require.NoError(t, res.Err)
	assert.Equal(t, FactVerdictDelete, res.Verdict)
	assert.Equal(t, "speculative", res.Rule)
}
