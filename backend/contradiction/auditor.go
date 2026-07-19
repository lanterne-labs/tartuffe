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

	"github.com/rs/zerolog/log"
)

// contradictionAuditAllowedRules is the closed set of RULE tokens the
// contradiction-auditor prompt asks the model to return. We are deliberately
// strict — anything outside this set triggers a corrective retry; we do NOT
// fuzzy-match variants because that would silently hide cases where the LLM
// is not actually reasoning under our enum.
var contradictionAuditAllowedRules = map[string]bool{
	"none":                         true,
	"accusation_not_finding":       true,
	"learning_over_time":           true,
	"paraphrase_same_position":     true,
	"geographic_scope":             true,
	"coherent_defense":             true,
	"conviction_vs_acknowledgment": true,
	"announcement_then_action":     true,
	"different_subjects":           true,
	"different_events":             true,
	"subjective_vs_objective":      true,
	"consistent_ignorance":         true,
	"legal_process_stages":         true,
	"complementary_facts":          true,
	"vague_vs_specific":            true,
	"third_party_narrative":        true,
	"date_only_no_commitment":      true,
	"symbolic_counter_gesture":     true,
	// Added with the shared promptrules.FalsePositivePatterns catalog — these
	// buckets came from the verifier and must be nameable by the auditor too.
	"numbers_measurements":         true,
	"forecast_vs_current":          true,
	"procedural_scope":             true,
	"condition_later_met":          true,
	"same_direction":               true,
	"endorsement_vs_non_candidacy": true,
}

// contradictionAuditAllowedRulesList is the alphabetised version used in the
// strict-retry prompt, so the model sees a stable, ordered list.
var contradictionAuditAllowedRulesList = []string{
	"none",
	"accusation_not_finding",
	"announcement_then_action",
	"coherent_defense",
	"complementary_facts",
	"condition_later_met",
	"consistent_ignorance",
	"conviction_vs_acknowledgment",
	"date_only_no_commitment",
	"different_events",
	"different_subjects",
	"endorsement_vs_non_candidacy",
	"forecast_vs_current",
	"geographic_scope",
	"learning_over_time",
	"legal_process_stages",
	"numbers_measurements",
	"paraphrase_same_position",
	"procedural_scope",
	"same_direction",
	"subjective_vs_objective",
	"symbolic_counter_gesture",
	"third_party_narrative",
	"vague_vs_specific",
}

// validateContradictionAuditRule strictly checks that `raw` is in the closed
// enum (after lower/trim). Returns the canonical token on success or an error
// describing the violation so the caller can issue a corrective retry.
func validateContradictionAuditRule(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(raw))
	if s == "" {
		return "", fmt.Errorf("RULE field is empty (must be one of: %s)", strings.Join(contradictionAuditAllowedRulesList, ", "))
	}
	if !contradictionAuditAllowedRules[s] {
		return "", fmt.Errorf("RULE %q is not in the closed enum (must be one of: %s)", raw, strings.Join(contradictionAuditAllowedRulesList, ", "))
	}
	return s, nil
}

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

// auditorKeptExamples lists the worked examples retained in the auditor's
// prompt, by their header prefix in ContradictionRulesAndExamples. The full
// body pushes the audit prompt to ~10K words, and that bloat measurably
// degrades judgment: the same local model judges FP_177/TP_114-shaped cases
// correctly under the ~1.5K-word verifier prompt and wrongly under the full
// audit prompt (CHANGES.md 2026-07-17). The false-positive knowledge is
// already carried by the embedded FalsePositivePatterns catalog, so the
// auditor keeps ONE example per decision family. The batch detector keeps the
// full example set — its raw-pair task is the one the examples were written
// for, and its prompt is not the one misjudging.
var auditorKeptExamples = []string{
	// keep side (real contradictions the auditor must never evict)
	"PAIR 2 (",  // denial after claim
	"PAIR 9 (",  // court finding vs denial
	"PAIR 30 (", // values-based / directional axis
	// remove side (false-positive families)
	"PAIR 1 (",  // learning over time
	"PAIR 8 (",  // trial/accusation is not a finding
	"PAIR 15 (", // conviction vs acknowledgment of imperfections
	"PAIR 18 (", // same position in different words (double negatives)
	"PAIR 25 (", // announcement then action (fulfilled commitment)
	"PAIR 27 (", // third-party narratives only
	"PAIR 28 (", // date-only conflict, no commitment
}

// leanAuditRules returns ContradictionRulesAndExamples with the worked-example
// tail filtered down to auditorKeptExamples. Derived from the single shared
// source at init so the rule text itself can never drift from the detector's;
// only the example SELECTION differs. Falls back to the full body if the
// EXAMPLES marker ever moves.
func leanAuditRules() string {
	const marker = "**EXAMPLES:**"
	i := strings.Index(ContradictionRulesAndExamples, marker)
	if i < 0 {
		return ContradictionRulesAndExamples
	}
	head := ContradictionRulesAndExamples[:i+len(marker)]
	blocks := strings.Split(ContradictionRulesAndExamples[i+len(marker):], "\n\n")
	kept := make([]string, 0, len(auditorKeptExamples))
	for _, b := range blocks {
		t := strings.TrimSpace(b)
		if t == "" {
			continue
		}
		if !strings.HasPrefix(t, "PAIR ") {
			// Non-example chunk inside the tail (none today) — keep, fail-safe.
			kept = append(kept, t)
			continue
		}
		for _, want := range auditorKeptExamples {
			if strings.HasPrefix(t, want) {
				kept = append(kept, t)
				break
			}
		}
	}
	return head + "\n\n" + strings.Join(kept, "\n\n")
}

// auditPromptTpl is the audit prompt. The %s is replaced by the audit block
// (the two facts and the original detection reason). The rule body is shared
// with the detector via ContradictionRulesAndExamples so audit and detection
// cannot drift in their definition of a contradiction — the auditor just sees
// a curated subset of the worked examples (see auditorKeptExamples).
var auditPromptTpl = `You are auditing previously-flagged contradictions to remove false positives.

An earlier automated pass marked the two statements below as contradictory, with the reason given. Re-evaluate STRICTLY using the rules and examples below — would you flag this today?

Be CONSERVATIVE: when in doubt, return "ambiguous" rather than "keep". Only return "keep" when the contradiction is unambiguous under the rules. Return "remove" when the rules clearly indicate the pair should NOT have been flagged (false positive).

**NEVER REMOVE A GENUINE CONTRADICTION.** Check these REAL-contradiction patterns FIRST — if the pair matches one, the verdict is "keep", NOT "remove", even when it superficially resembles a false-positive bucket below:
- POSITION / ATTITUDE REVERSAL: the person stated, announced, or pleaded FOR X, then stated, announced, or did the OPPOSITE about the same subject — e.g. "I will run" then "I will not run"; "I won't be a candidate" then leading/joining a winning list at that election; "very motivated" then "not enthusiastic"; pleads FOR a primary then is AGAINST one. This is a reversal — do NOT file it under announcement_then_action, different_events, vague_vs_specific, same_direction, or different_subjects.
- COMMITMENT vs CONTRARY ACTION: a promise, value, or commitment broken by the person's own opposing action (e.g. promised to keep the retirement age at 62, then abandoned the repeal that would have undone the move to 64).
- DENIAL vs AUTHORITATIVE FINDING: a personal denial contradicted by a conviction or official investigation conclusion in the same matter.
- DENOUNCE-THEN-ALLY: publicly blaming or attacking a SPECIFIC NAMED person or party, then allying, merging lists, or partnering with that SAME target. The shared subject is that NAMED person, so this is never different_subjects: do NOT excuse it with "blame and alliance are different topics" or "attributing responsibility to him is not logically incompatible with a later alliance with him".
- SELF-EXEMPTING DOUBLE STANDARD: the person demanded a rule or sanction for a CLASS of people ("anyone convicted should be ineligible / must resign / be barred / be punished"), then — once they themselves demonstrably fall into that class (e.g. they were themselves convicted) — exempt themselves from that same sanction by their own action or claim (running for or keeping the office). Their class-membership may be established by the context in the detection reason. Do NOT file this under different_subjects, vague_vs_specific, or complementary_facts, and do NOT excuse it with "a general normative position is not a personal commitment" — the shared subject is that ONE sanction and they are inside the very class they named, so the rule binds them. Example (verdict keep, RULE none): "il faudrait rendre inéligibles à vie ceux qui étaient condamnés" (2013) vs her own claim to be a candidate while she is herself convicted.
Reported speech of the person ("X plaide / affirme / a dit / a annoncé Y") is the person's OWN position or claim — it is NOT third_party_narrative.

` + leanAuditRules() + `

**CONTRADICTION TO AUDIT:**
%s

**AUDIT RESPONSE FORMAT:**
Respond with EXACTLY three lines, in this order:

REASONING: [1-2 sentences identifying the core claims and which rule(s) apply]
RULE: [exactly one token, no spaces, no quotes — see closed list below]
VERDICT: [keep|remove|ambiguous]

⚠️ STRICT FORMATTING — the RULE field MUST be EXACTLY ONE of these tokens (lowercase snake_case, no extra words, no parentheses, no rule numbers):
%s

If VERDICT is "keep", RULE MUST be "none". Otherwise return EXACTLY one token from the list above. Do NOT invent variants. Your verdict MUST follow logically from your reasoning. If your reasoning concludes the pair is compatible/supplementary/paraphrastic, your verdict MUST be "remove".

**LANGUAGE RULE:** Write your REASONING in the same language as the statements being audited. If statements span multiple languages, write in English.`

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

	apiURL := llm.ChatCompletionsURL(base)

	return &OpenAIContradictionAuditor{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      model,
		baseClient: llm.NewBaseLLMClient("contradiction-auditor"),
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
	// The allowed-token list is generated from the single-source enum slice so
	// the prompt can never drift from validateContradictionAuditRule.
	ruleTokens := "  " + strings.Join(contradictionAuditAllowedRulesList, "\n  ")
	prompt := fmt.Sprintf(auditPromptTpl, auditBlock, ruleTokens)
	messages := []llm.ChatMessage{{Role: "user", Content: prompt}}

	llmText, err := a.callChat(ctx, messages)
	if err != nil {
		return AuditResult{Err: err}
	}
	res := parseAuditResponse(llmText)
	res.RawResp = llmText
	if res.Err == nil {
		return res
	}

	// Strict retry: surface what was wrong so the model can correct itself.
	log.Warn().Err(res.Err).Str("raw_response", llmText).
		Msg("Contradiction audit: first response failed strict parse; issuing corrective retry")
	corrective := fmt.Sprintf(
		`Your previous reply was rejected: %s.

Reply ONCE MORE with EXACTLY three lines, in this order, and nothing else:

REASONING: <1-2 sentences>
RULE: <one token from this list ONLY: %s>
VERDICT: <keep|remove|ambiguous>

If VERDICT is "keep", RULE MUST be "none". Do NOT add commentary, headers, code fences, or any text outside the three required lines.`,
		res.Err.Error(), strings.Join(contradictionAuditAllowedRulesList, " | "),
	)
	messages = append(messages,
		llm.ChatMessage{Role: "assistant", Content: llmText},
		llm.ChatMessage{Role: "user", Content: corrective},
	)
	llmText2, err := a.callChat(ctx, messages)
	if err != nil {
		return AuditResult{
			RawResp: llmText,
			Err:     fmt.Errorf("strict retry failed: %w (first reply: %q)", err, llmText),
		}
	}
	res2 := parseAuditResponse(llmText2)
	res2.RawResp = llmText + "\n---RETRY---\n" + llmText2
	if res2.Err != nil {
		res2.Err = fmt.Errorf("LLM returned malformed response twice (strict): %w (first reply: %q, second reply: %q)", res2.Err, llmText, llmText2)
	}
	return res2
}

// callChat marshals + executes a single chat-completion call and returns the
// content of the first choice. Shared between the initial call and the strict
// corrective retry.
func (a *OpenAIContradictionAuditor) callChat(ctx context.Context, messages []llm.ChatMessage) (string, error) {
	reqPayload := llm.ChatRequest{
		Model:       a.model,
		Messages:    messages,
		Temperature: 0.1,
		// 3-line response (REASONING/RULE/VERDICT) is ~150 tokens. 1024 leaves
		// headroom and keeps prompt+reservation well under any 8k ctx.
		MaxTokens: 1024,
	}
	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("marshal audit request: %w", err)
	}
	bodyBytes, err := a.baseClient.DoRequest(ctx, a.apiURL, a.apiKey, payloadBytes)
	if err != nil {
		return "", fmt.Errorf("audit API call failed: %w", err)
	}
	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return "", fmt.Errorf("decode audit response: %w (raw: %s)", err, string(bodyBytes))
	}
	if chatResp.Error != nil {
		return "", fmt.Errorf("audit API error: %s", chatResp.Error.Message)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("audit API returned no content (raw: %s)", string(bodyBytes))
	}
	return chatResp.Choices[0].Message.Content, nil
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
	canonRule, ruleErr := validateContradictionAuditRule(rule)
	if ruleErr != nil {
		return AuditResult{
			Verdict:   verdict,
			Reasoning: reasoning,
			Rule:      rule,
			Err:       ruleErr,
		}
	}
	if verdict == VerdictKeep && canonRule != "none" {
		return AuditResult{
			Verdict:   verdict,
			Reasoning: reasoning,
			Rule:      canonRule,
			Err:       fmt.Errorf("VERDICT=keep requires RULE=none (got %q)", canonRule),
		}
	}
	if verdict != VerdictKeep && canonRule == "none" {
		return AuditResult{
			Verdict:   verdict,
			Reasoning: reasoning,
			Rule:      canonRule,
			Err:       fmt.Errorf("VERDICT=%s requires a non-'none' RULE", verdict),
		}
	}
	return AuditResult{
		Verdict:   verdict,
		Reasoning: reasoning,
		Rule:      canonRule,
	}
}
