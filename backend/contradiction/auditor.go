package contradiction

import (
	"context"
	"digital-mirror/backend/llm"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"
)

// AuditVerdict is the auditor's judgement on an existing contradiction record.
type AuditVerdict string

const (
	VerdictKeep      AuditVerdict = "keep"      // contradiction is solid, leave it in DB
	VerdictRemove    AuditVerdict = "remove"    // false positive, should be deleted
	VerdictAmbiguous AuditVerdict = "ambiguous" // unclear, manual review needed
)

// AuditResult is the structured outcome of auditing one stored contradiction.
type AuditResult struct {
	Verdict   AuditVerdict
	Reasoning string // 1-2 sentence justification
	Rule      string // which rule applied (e.g. "accusation_not_finding"), or "none"
	RawResp   string // full LLM response for inspection
	Err       error
}

// auditPromptTpl is the audit prompt. The %s is replaced by the audit block
// (the two facts and the original detection reason). The rule body is shared
// with the detector via ContradictionRulesAndExamples so audit and detection
// cannot drift in their definition of a contradiction.
const auditPromptTpl = `You are auditing previously-flagged contradictions to remove false positives.

An earlier automated pass marked the two statements below as contradictory, with the reason given. Re-evaluate STRICTLY using the rules and examples below — would you flag this today?

Be CONSERVATIVE: when in doubt, return "ambiguous" rather than "keep". Only return "keep" when the contradiction is unambiguous under the rules. Return "remove" when the rules clearly indicate the pair should NOT have been flagged (false positive).

` + ContradictionRulesAndExamples + `

**CONTRADICTION TO AUDIT:**
%s

**AUDIT RESPONSE FORMAT:**
Respond with EXACTLY three lines, in this order:

REASONING: [1-2 sentences identifying the core claims and which rule(s) apply]
RULE: [short snake_case label of the rule that drove your verdict — e.g. accusation_not_finding, learning_over_time, paraphrase_same_position, geographic_scope, coherent_defense, conviction_vs_acknowledgment, announcement_then_action, none]
VERDICT: [keep|remove|ambiguous]

Your verdict MUST follow logically from your reasoning. If your reasoning concludes the pair is compatible/supplementary/paraphrastic, your verdict MUST be "remove".`

var (
	auditReasoningRegex = regexp.MustCompile(`(?i)^\s*REASONING\s*:\s*(.*)$`)
	auditRuleRegex      = regexp.MustCompile(`(?i)^\s*RULE\s*:\s*(.*)$`)
	auditVerdictRegex   = regexp.MustCompile(`(?i)^\s*VERDICT\s*:\s*(keep|remove|ambiguous)\b`)
)

// OpenAIContradictionAuditor calls an OpenAI-compatible chat API to audit a
// single recorded contradiction. Unlike the detector it does not batch — audits
// are run ad-hoc by a human and verdict independence is more important than
// throughput.
type OpenAIContradictionAuditor struct {
	apiKey     string
	apiURL     string
	model      string
	baseClient *llm.BaseLLMClient
}

// NewOpenAIContradictionAuditorFromEnv builds an auditor from the same env
// vars as the detector: OPEN_AI_KEY, OPEN_AI_API, OPEN_AI_CHAT_MODEL.
func NewOpenAIContradictionAuditorFromEnv() (*OpenAIContradictionAuditor, error) {
	apiKey := os.Getenv("OPEN_AI_KEY")
	if apiKey == "" {
		return nil, fmt.Errorf("OPEN_AI_KEY environment variable not set")
	}
	base := os.Getenv("OPEN_AI_API")
	if base == "" {
		return nil, fmt.Errorf("OPEN_AI_API environment variable not set")
	}
	model := os.Getenv("OPEN_AI_CHAT_MODEL")
	if strings.TrimSpace(model) == "" {
		model = "gpt-4o-mini"
	}

	base = strings.TrimRight(base, "/")
	apiURL := base
	if !strings.HasSuffix(base, "/chat/completions") {
		apiURL = base + "/chat/completions"
	}

	return &OpenAIContradictionAuditor{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      model,
		baseClient: llm.NewBaseLLMClient(),
	}, nil
}

// Audit evaluates a single recorded contradiction.
func (a *OpenAIContradictionAuditor) Audit(ctx context.Context, factA, factB string, dateA, dateB *time.Time, originalReason string) AuditResult {
	if factA == factB {
		return AuditResult{
			Verdict:   VerdictRemove,
			Reasoning: "Both statements are textually identical; an identical pair cannot be a contradiction.",
			Rule:      "paraphrase_same_position",
		}
	}

	auditBlock := fmt.Sprintf(
		"Statement A (dated %s): %q\nStatement B (dated %s): %q\nOriginal detection reason: %q",
		formatDateForLLM(dateA), factA,
		formatDateForLLM(dateB), factB,
		originalReason,
	)
	prompt := fmt.Sprintf(auditPromptTpl, auditBlock)

	reqPayload := llm.ChatRequest{
		Model: a.model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.1,
		MaxTokens:   8192,
	}
	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return AuditResult{Err: fmt.Errorf("marshal audit request: %w", err)}
	}

	bodyBytes, err := a.baseClient.DoRequest(ctx, a.apiURL, a.apiKey, payloadBytes)
	if err != nil {
		return AuditResult{Err: fmt.Errorf("audit API call failed: %w", err)}
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return AuditResult{Err: fmt.Errorf("decode audit response: %w (raw: %s)", err, string(bodyBytes))}
	}
	if chatResp.Error != nil {
		return AuditResult{Err: fmt.Errorf("audit API error: %s", chatResp.Error.Message)}
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return AuditResult{Err: fmt.Errorf("audit API returned no content (raw: %s)", string(bodyBytes))}
	}

	llmText := chatResp.Choices[0].Message.Content
	res := parseAuditResponse(llmText)
	res.RawResp = llmText
	return res
}

// parseAuditResponse extracts REASONING / RULE / VERDICT lines.
func parseAuditResponse(response string) AuditResult {
	var (
		reasoning, rule string
		verdict         AuditVerdict
	)
	for _, line := range strings.Split(response, "\n") {
		if m := auditReasoningRegex.FindStringSubmatch(line); m != nil && reasoning == "" {
			reasoning = strings.TrimSpace(m[1])
			continue
		}
		if m := auditRuleRegex.FindStringSubmatch(line); m != nil && rule == "" {
			rule = strings.TrimSpace(m[1])
			continue
		}
		if m := auditVerdictRegex.FindStringSubmatch(line); m != nil && verdict == "" {
			verdict = AuditVerdict(strings.ToLower(strings.TrimSpace(m[1])))
		}
	}

	if verdict == "" {
		return AuditResult{
			Reasoning: reasoning,
			Rule:      rule,
			Err:       fmt.Errorf("could not parse VERDICT line from audit response: %q", response),
		}
	}
	return AuditResult{
		Verdict:   verdict,
		Reasoning: reasoning,
		Rule:      rule,
	}
}
