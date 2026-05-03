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
		"RULE: unknown_dates",
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

func TestParseAuditResponse_OnlyFirstVerdictWins(t *testing.T) {
	resp := strings.Join([]string{
		"REASONING: First reasoning.",
		"RULE: first_rule",
		"VERDICT: keep",
		"REASONING: Second reasoning.",
		"VERDICT: remove",
	}, "\n")
	res := parseAuditResponse(resp)
	require.NoError(t, res.Err)
	assert.Equal(t, VerdictKeep, res.Verdict)
	assert.Equal(t, "First reasoning.", res.Reasoning)
	assert.Equal(t, "first_rule", res.Rule)
}
