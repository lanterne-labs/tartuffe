package llm

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/rs/zerolog/log"
)

// VerdictJudge runs a closed-enum REASONING / RULE / VERDICT judgement: one
// chat call, a strict parse, and at most one corrective retry that quotes the
// violation back to the model. Both auditors (stored facts, stored
// contradictions) are thin over it; only the enums, the prompt and the
// sampling parameters differ.
//
// The parse is strict on purpose: a RULE outside the closed enum is a parse
// error, never normalised. Tolerant matching hid extraction bugs and produced
// silently-wrong rule frequencies in the dashboards; strict-or-retry is the
// contract.
type VerdictJudge struct {
	Client *Client
	// Name prefixes the corrective-retry warning, e.g. "Fact audit".
	Name string
	// Verdicts is the closed VERDICT enum. Verdicts[0] is the "nothing applies"
	// verdict, the only one allowed (and required) to pair with Rules[0].
	Verdicts []string
	// Rules is the closed RULE enum in the order the retry prompt quotes it.
	// Rules[0] is the null rule.
	Rules []string
	// UnlistedRules are accepted by Parse but never quoted in the retry
	// prompt: tokens the rulebook text names without the advertised list
	// having caught up. Adding one to Rules changes the prompt bytes.
	UnlistedRules []string
	Temperature   float64
	MaxTokens     int
}

// Verdict is one parsed judgement. On a parse error the fields hold whatever
// was recovered (Rule keeps the raw offending value so the retry can quote it).
type Verdict struct {
	Verdict   string
	Reasoning string
	Rule      string
	// RawResp is the first reply, followed by "\n---RETRY---\n" and the second
	// reply when a corrective retry was issued.
	RawResp string
}

var (
	verdictReasoningRegex = regexp.MustCompile(`(?i)^\s*REASONING\s*:\s*(.*)$`)
	verdictRuleRegex      = regexp.MustCompile(`(?i)^\s*RULE\s*:\s*(.*)$`)
)

const verdictCorrectiveTpl = `Your previous reply was rejected: %s.

Reply ONCE MORE with EXACTLY three lines, in this order, and nothing else:

REASONING: <1-2 sentences>
RULE: <one token from this list ONLY: %s>
VERDICT: <%s>

If VERDICT is "%s", RULE MUST be "%s". Do NOT add commentary, headers, code fences, or any text outside the three required lines.`

// Judge sends prompt and parses the reply. If the first reply is malformed
// (missing VERDICT line, RULE outside the closed enum, verdict/rule
// cross-check failed) it sends ONE strict corrective follow-up, with the bad
// reply appended as the assistant turn so the model sees what to fix, and
// re-parses. A second bad reply surfaces the parse error so the caller can log
// or mark the row rather than swallow garbage.
func (j *VerdictJudge) Judge(ctx context.Context, prompt string) (Verdict, error) {
	messages := []ChatMessage{{Role: "user", Content: prompt}}

	first, err := j.call(ctx, messages)
	if err != nil {
		return Verdict{}, err
	}
	res, err := j.Parse(first)
	res.RawResp = first
	if err == nil {
		return res, nil
	}

	log.Warn().Err(err).Str("raw_response", first).
		Msg(j.Name + ": first response failed strict parse; issuing corrective retry")
	corrective := fmt.Sprintf(verdictCorrectiveTpl,
		err.Error(), strings.Join(j.Rules, " | "), strings.Join(j.Verdicts, "|"), j.Verdicts[0], j.Rules[0])
	messages = append(messages,
		ChatMessage{Role: "assistant", Content: first},
		ChatMessage{Role: "user", Content: corrective},
	)
	second, err := j.call(ctx, messages)
	if err != nil {
		return Verdict{RawResp: first}, fmt.Errorf("strict retry failed: %w (first reply: %q)", err, first)
	}
	res2, err := j.Parse(second)
	res2.RawResp = first + "\n---RETRY---\n" + second
	if err != nil {
		return res2, fmt.Errorf("LLM returned malformed response twice (strict): %w (first reply: %q, second reply: %q)", err, first, second)
	}
	return res2, nil
}

func (j *VerdictJudge) call(ctx context.Context, messages []ChatMessage) (string, error) {
	content, _, err := j.Client.Chat(ctx, ChatRequest{
		Messages:    messages,
		Temperature: j.Temperature,
		MaxTokens:   j.MaxTokens,
	})
	if err != nil {
		return "", fmt.Errorf("judge API call failed: %w", err)
	}
	return content, nil
}

// Parse extracts the REASONING / RULE / VERDICT lines (first occurrence of
// each wins), validates RULE against the closed enum and cross-checks it with
// the verdict: Verdicts[0] requires Rules[0], every other verdict requires a
// real rule. The error text is what the corrective retry quotes to the model.
func (j *VerdictJudge) Parse(response string) (Verdict, error) {
	verdictRegex := regexp.MustCompile(`(?i)^\s*VERDICT\s*:\s*(` + strings.Join(j.Verdicts, "|") + `)\b`)
	var reasoning, rule, verdict string
	for _, line := range strings.Split(response, "\n") {
		if m := verdictReasoningRegex.FindStringSubmatch(line); m != nil && reasoning == "" {
			reasoning = strings.TrimSpace(m[1])
			continue
		}
		if m := verdictRuleRegex.FindStringSubmatch(line); m != nil && rule == "" {
			rule = strings.TrimSpace(m[1])
			continue
		}
		if m := verdictRegex.FindStringSubmatch(line); m != nil && verdict == "" {
			verdict = strings.ToLower(strings.TrimSpace(m[1]))
		}
	}

	if verdict == "" {
		return Verdict{Reasoning: reasoning, Rule: rule},
			fmt.Errorf("could not parse VERDICT line from audit response: %q", response)
	}
	canonRule, err := j.validateRule(rule)
	if err != nil {
		return Verdict{Verdict: verdict, Reasoning: reasoning, Rule: rule}, err
	}
	res := Verdict{Verdict: verdict, Reasoning: reasoning, Rule: canonRule}
	if verdict == j.Verdicts[0] && canonRule != j.Rules[0] {
		return res, fmt.Errorf("VERDICT=%s requires RULE=%s (got %q)", j.Verdicts[0], j.Rules[0], canonRule)
	}
	if verdict != j.Verdicts[0] && canonRule == j.Rules[0] {
		return res, fmt.Errorf("VERDICT=%s requires a non-'%s' RULE", verdict, j.Rules[0])
	}
	return res, nil
}

// validateRule checks that raw (after lower/trim) is in the closed enum and
// returns the canonical token, or an error describing the violation.
func (j *VerdictJudge) validateRule(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", fmt.Errorf("RULE field is empty (must be one of: %s)", strings.Join(j.Rules, ", "))
	}
	for _, r := range j.Rules {
		if s == r {
			return s, nil
		}
	}
	for _, r := range j.UnlistedRules {
		if s == r {
			return s, nil
		}
	}
	return "", fmt.Errorf("RULE %q is not in the closed enum (must be one of: %s)", raw, strings.Join(j.Rules, ", "))
}
