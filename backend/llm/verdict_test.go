package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testJudge mirrors the contradiction auditor's enums; the parser is
// enum-agnostic so one fixture covers both auditors.
func testJudge() *VerdictJudge {
	return &VerdictJudge{
		Name:     "Test audit",
		Verdicts: []string{"keep", "remove", "ambiguous"},
		Rules:    []string{"none", "accusation_not_finding", "different_events", "geographic_scope", "learning_over_time", "paraphrase_same_position", "vague"},
	}
}

func TestVerdictParse_Keep(t *testing.T) {
	res, err := testJudge().Parse(strings.Join([]string{
		"REASONING: Both statements directly contradict on the person's own knowledge of the same specific event.",
		"RULE: none",
		"VERDICT: keep",
	}, "\n"))
	require.NoError(t, err)
	assert.Equal(t, "keep", res.Verdict)
	assert.Equal(t, "none", res.Rule)
	assert.Contains(t, res.Reasoning, "directly contradict")
}

func TestVerdictParse_Remove(t *testing.T) {
	res, err := testJudge().Parse(strings.Join([]string{
		"REASONING: Statement A is an indictment, statement B is a denial.",
		"RULE: accusation_not_finding",
		"VERDICT: remove",
	}, "\n"))
	require.NoError(t, err)
	assert.Equal(t, "remove", res.Verdict)
	assert.Equal(t, "accusation_not_finding", res.Rule)
}

func TestVerdictParse_ThirdVerdict(t *testing.T) {
	res, err := testJudge().Parse("REASONING: Both dates unknown.\nRULE: different_events\nVERDICT: ambiguous")
	require.NoError(t, err)
	assert.Equal(t, "ambiguous", res.Verdict)
}

func TestVerdictParse_CaseInsensitiveAndExtraWhitespace(t *testing.T) {
	res, err := testJudge().Parse(strings.Join([]string{
		"   reasoning :  Different geographic scope.",
		"  Rule:  geographic_scope",
		"  verdict :  REMOVE  with explanation",
	}, "\n"))
	require.NoError(t, err)
	assert.Equal(t, "remove", res.Verdict)
	assert.Equal(t, "geographic_scope", res.Rule)
}

func TestVerdictParse_NoiseBeforeAndAfter(t *testing.T) {
	res, err := testJudge().Parse(strings.Join([]string{
		"Sure, here's my analysis:",
		"",
		"REASONING: Paraphrase of the same position.",
		"RULE: paraphrase_same_position",
		"VERDICT: remove",
		"",
		"Let me know if you need more detail.",
	}, "\n"))
	require.NoError(t, err)
	assert.Equal(t, "remove", res.Verdict)
	assert.Contains(t, res.Reasoning, "Paraphrase")
}

func TestVerdictParse_MissingVerdictReturnsError(t *testing.T) {
	res, err := testJudge().Parse("REASONING: I think this might be a contradiction.\nRULE: none")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "could not parse VERDICT line")
	assert.Equal(t, "", res.Verdict)
	assert.Equal(t, "none", res.Rule)
}

// Strict-mode contract: a RULE token outside the closed enum is rejected with
// an error so the auditor can issue a corrective retry. Variants are never
// fuzzy-matched: silent normalisation hid extraction bugs in earlier runs and
// produced unstable rule frequencies in metrics.
func TestVerdictParse_RejectsUnknownRule(t *testing.T) {
	res, err := testJudge().Parse("REASONING: not in our enum.\nRULE: vague_or_not_concrete (rule 6)\nVERDICT: remove")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed enum")
	// Verdict is still parsed; Rule surfaces the bad raw value so the retry
	// prompt can quote it.
	assert.Equal(t, "remove", res.Verdict)
	assert.Equal(t, "vague_or_not_concrete (rule 6)", res.Rule)
}

func TestVerdictParse_RejectsRuleWithTrailingProse(t *testing.T) {
	_, err := testJudge().Parse("REASONING: different geographic scope.\nRULE: geographic_scope (the fact about Vienne is not national)\nVERDICT: remove")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "closed enum")
}

// An unlisted rule is accepted on parse but absent from the enum the error
// message (and therefore the retry prompt) quotes.
func TestVerdictParse_UnlistedRule(t *testing.T) {
	j := testJudge()
	j.UnlistedRules = []string{"relative_vote_preference"}
	res, err := j.Parse("REASONING: x.\nRULE: relative_vote_preference\nVERDICT: remove")
	require.NoError(t, err)
	assert.Equal(t, "relative_vote_preference", res.Rule)
	_, err = j.Parse("REASONING: x.\nRULE: bogus\nVERDICT: remove")
	require.Error(t, err)
	assert.NotContains(t, err.Error(), "relative_vote_preference")
}

func TestVerdictParse_EmptyRule(t *testing.T) {
	_, err := testJudge().Parse("REASONING: x.\nRULE:\nVERDICT: remove")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RULE field is empty")
}

func TestVerdictParse_KeepRequiresNone(t *testing.T) {
	_, err := testJudge().Parse("REASONING: actually contradictory.\nRULE: learning_over_time\nVERDICT: keep")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "VERDICT=keep requires RULE=none")
}

func TestVerdictParse_NonKeepRequiresRule(t *testing.T) {
	_, err := testJudge().Parse("REASONING: I think this is bad but cannot say why.\nRULE: none\nVERDICT: remove")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-'none'")
}

func TestVerdictParse_OnlyFirstVerdictWins(t *testing.T) {
	res, err := testJudge().Parse(strings.Join([]string{
		"REASONING: First reasoning.",
		"RULE: none",
		"VERDICT: keep",
		"REASONING: Second reasoning.",
		"VERDICT: remove",
	}, "\n"))
	require.NoError(t, err)
	assert.Equal(t, "keep", res.Verdict)
	assert.Equal(t, "First reasoning.", res.Reasoning)
	assert.Equal(t, "none", res.Rule)
}

// TestVerdictJudge_CorrectiveRetry drives the full Judge flow against a fake
// endpoint: a malformed first reply must produce exactly one corrective turn
// that quotes the parse error, echoes the bad reply as the assistant message,
// and keeps the sampling parameters; RawResp joins both replies.
func TestVerdictJudge_CorrectiveRetry(t *testing.T) {
	defer resetBreaker()
	resetBreaker()
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("CF_ACCESS_CLIENT_ID", "")

	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		w.Header().Set("Content-Type", "application/json")
		if len(bodies) == 1 {
			_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"REASONING: r.\nRULE: bogus\nVERDICT: remove\n"}}]}`)
			return
		}
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"REASONING: r.\nRULE: vague\nVERDICT: remove"}}]}`)
	}))
	defer srv.Close()

	j := testJudge()
	j.Client = Config{BaseURL: srv.URL, APIKey: "k", ChatModel: "m"}.NewClient("test")
	j.Client.httpClient = &http.Client{Timeout: 2 * time.Second}
	j.Temperature = 0.1
	j.MaxTokens = 1024

	res, err := j.Judge(context.Background(), "PROMPT")
	require.NoError(t, err)
	assert.Equal(t, "remove", res.Verdict)
	assert.Equal(t, "vague", res.Rule)
	assert.Equal(t, "REASONING: r.\nRULE: bogus\nVERDICT: remove\n\n---RETRY---\nREASONING: r.\nRULE: vague\nVERDICT: remove", res.RawResp)
	require.Len(t, bodies, 2)

	var retry ChatRequest
	require.NoError(t, json.Unmarshal(bodies[1], &retry))
	assert.Equal(t, "m", retry.Model)
	assert.Equal(t, 0.1, retry.Temperature)
	assert.Equal(t, 1024, retry.MaxTokens)
	require.Len(t, retry.Messages, 3)
	assert.Equal(t, ChatMessage{Role: "user", Content: "PROMPT"}, retry.Messages[0])
	assert.Equal(t, ChatMessage{Role: "assistant", Content: "REASONING: r.\nRULE: bogus\nVERDICT: remove\n"}, retry.Messages[1])
	assert.Equal(t, "user", retry.Messages[2].Role)
	assert.True(t, strings.HasPrefix(retry.Messages[2].Content, `Your previous reply was rejected: RULE "bogus" is not in the closed enum (must be one of: none, accusation_not_finding, different_events, geographic_scope, learning_over_time, paraphrase_same_position, vague).`), retry.Messages[2].Content)
	assert.Contains(t, retry.Messages[2].Content, "RULE: <one token from this list ONLY: none | accusation_not_finding | different_events | geographic_scope | learning_over_time | paraphrase_same_position | vague>\nVERDICT: <keep|remove|ambiguous>\n\nIf VERDICT is \"keep\", RULE MUST be \"none\".")
}

// A second malformed reply surfaces the parse error with both replies quoted
// instead of a silent default verdict.
func TestVerdictJudge_TwiceMalformed(t *testing.T) {
	defer resetBreaker()
	resetBreaker()
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("CF_ACCESS_CLIENT_ID", "")

	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"no verdict here"}}]}`)
	}))
	defer srv.Close()

	j := testJudge()
	j.Client = Config{BaseURL: srv.URL, APIKey: "k", ChatModel: "m"}.NewClient("test")
	j.Client.httpClient = &http.Client{Timeout: 2 * time.Second}

	res, err := j.Judge(context.Background(), "PROMPT")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "malformed response twice")
	assert.Equal(t, 2, calls)
	assert.Equal(t, "no verdict here\n---RETRY---\nno verdict here", res.RawResp)
}
