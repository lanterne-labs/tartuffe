package core

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

// FactAuditVerdict is the auditor's judgement on an existing fact row.
type FactAuditVerdict string

const (
	FactVerdictKeep   FactAuditVerdict = "keep"   // fact is solid, leave it in DB
	FactVerdictDelete FactAuditVerdict = "delete" // fact violates one of the REJECT rules, should be removed
	FactVerdictUnsure FactAuditVerdict = "unsure" // borderline; manual review needed
)

// FactAuditInput is the per-fact payload handed to the auditor.
// All fields except NormalizedFact may be empty if the column is null in the DB.
type FactAuditInput struct {
	NormalizedFact    string
	CanonicalVerb     string
	EmbeddingText     string
	SubjectEntity     string // person/party name as known from fact_mentions
	OriginalQuote     string
	SourceURL         string
	SourcePublishedAt *time.Time // earliest source publication date, if known
	FactCreatedAt     *time.Time // when we ingested this fact (always present)
}

// FactAuditResult is the structured outcome of auditing one stored fact.
type FactAuditResult struct {
	Verdict   FactAuditVerdict
	Reasoning string // 1-2 sentence justification
	Rule      string // which REJECT rule applied (snake_case label) or "none"
	RawResp   string // full LLM response for inspection
	Err       error
}

// auditFactPromptTpl wraps the WHOLE CoreFactExtractionPrompt verbatim and reframes
// the task as auditing an EXISTING fact rather than extracting new ones. The
// original extraction prompt body is reproduced as a reference block so the rules
// (especially REJECT rules 1-7 + 5b pattern-of-behavior) cannot drift between
// extractor and auditor: when the extractor prompt is updated, the audit
// automatically picks up the new rules.
//
// Format slots:
//
//	%s — verbatim original extraction prompt (rendered via BuildFactExtractionPrompt
//	     with audit-mode placeholders for primary subject / source URL / content).
//	%s — formatted fact-under-audit block (subject, normalized_fact, etc.).
const auditFactPromptTpl = `You are auditing facts that were previously extracted from news articles. Your job is to detect facts that VIOLATE the extraction rules and should be deleted.

The exact extraction prompt that produced these facts is reproduced below for reference. Read it carefully — your audit MUST apply the same rules, especially the REJECT rules (1-7, including 5b pattern-of-behavior). DO NOT extract new facts; you are evaluating ONE existing fact at the bottom.

==================== ORIGINAL EXTRACTION PROMPT (REFERENCE) ====================
%s
==================== END OF EXTRACTION PROMPT ====================

NOW: a previous run produced the fact below. Apply the rules above (especially the REJECT rules) and decide whether this fact should be KEPT, DELETED, or marked UNSURE.

FACT UNDER AUDIT:
%s

DECISION POLICY (read carefully):
- VERDICT delete — clearly violates one or more REJECT rules. Be specific: name the rule. Common reasons:
    * pattern_of_behavior (rule 5b): "X has repeatedly", "a multiplié", "régulièrement", "tends to", habits/patterns/meta-summaries
    * journalist_characterization (rule 5): subjective narrative framing without evidence
    * not_contradictable (rule 4): hopes, feelings, travel, biography, courtroom procedure
    * speculative (rule 3): "could", "may", "risks", future scheduling, OR a prediction whose action is performed by a third party / impersonal force ("Iran will fall", "the markets will crash"). EXCEPTION: a conditional whose post-"if" action is performed BY THE SUBJECT ("Trump said he will strike if Iran refuses") is a Type 1 commitment — KEEP it.
    * vague (rule 6): no numbers, no concrete action. NOTE: if the fact lacks a date IN THE TEXT but the audit block below provides "source_published_at" or "fact_created_at", the fact IS anchored in time — do NOT flag it vague for missing an inline date. Only flag vague when the action/claim itself is fuzzy ("pays generously", "supports the policy") with no specifics.
    * lawyers_as_subject (rule 2): defense team / Me / Maître as subject_entity
    * not_about_entity (rule 1): third-party opinion, opponents' claims, organisation the entity does not lead
    * polls_or_surveys (rule 7)
- VERDICT keep — clearly a Type 1 (own statement/promise/denial) or Type 2 (court ruling, vote, documented finding) fact, contradictable, and no REJECT rule applies.
- VERDICT unsure — borderline cases. Prefer "unsure" over "delete" when the rule violation is not obvious; deletion is destructive.

RESPONSE FORMAT — respond with EXACTLY three lines, in this order:

REASONING: [1-2 sentences identifying which rule(s) apply and why]
RULE: [snake_case rule label from the list above, or "none" if VERDICT is keep]
VERDICT: [keep|delete|unsure]

Your verdict MUST follow logically from your reasoning.`

var (
	factAuditReasoningRegex = regexp.MustCompile(`(?i)^\s*REASONING\s*:\s*(.*)$`)
	factAuditRuleRegex      = regexp.MustCompile(`(?i)^\s*RULE\s*:\s*(.*)$`)
	factAuditVerdictRegex   = regexp.MustCompile(`(?i)^\s*VERDICT\s*:\s*(keep|delete|unsure)\b`)
)

// OpenAIFactAuditor calls an OpenAI-compatible chat API to audit a single
// stored fact row. The constructor takes explicit endpoint/model so the CLI
// can override env values when probing local models (e.g. via Tailscale).
type OpenAIFactAuditor struct {
	apiKey     string
	apiURL     string
	model      string
	baseClient *llm.BaseLLMClient
}

// NewOpenAIFactAuditorFromEnv builds an auditor reading OPEN_AI_KEY,
// OPEN_AI_API and OPEN_AI_CHAT_MODEL from the environment.
func NewOpenAIFactAuditorFromEnv() (*OpenAIFactAuditor, error) {
	return NewOpenAIFactAuditor(os.Getenv("OPEN_AI_KEY"), os.Getenv("OPEN_AI_API"), os.Getenv("OPEN_AI_CHAT_MODEL"))
}

// NewOpenAIFactAuditor builds an auditor with explicit endpoint/model. Empty
// strings fall back to env vars (so callers can override only what they need).
func NewOpenAIFactAuditor(apiKey, apiURL, model string) (*OpenAIFactAuditor, error) {
	if strings.TrimSpace(apiKey) == "" {
		apiKey = os.Getenv("OPEN_AI_KEY")
	}
	if strings.TrimSpace(apiURL) == "" {
		apiURL = os.Getenv("OPEN_AI_API")
	}
	if strings.TrimSpace(model) == "" {
		model = os.Getenv("OPEN_AI_CHAT_MODEL")
	}
	if apiKey == "" {
		return nil, fmt.Errorf("OPEN_AI_KEY not set (and no --apikey override)")
	}
	if apiURL == "" {
		return nil, fmt.Errorf("OPEN_AI_API not set (and no --endpoint override)")
	}
	if strings.TrimSpace(model) == "" {
		model = "gpt-4o-mini"
	}

	apiURL = strings.TrimRight(apiURL, "/")
	if !strings.HasSuffix(apiURL, "/chat/completions") {
		apiURL = apiURL + "/chat/completions"
	}

	return &OpenAIFactAuditor{
		apiKey:     apiKey,
		apiURL:     apiURL,
		model:      model,
		baseClient: llm.NewBaseLLMClient(),
	}, nil
}

// Model returns the model id the auditor will use (for logging).
func (a *OpenAIFactAuditor) Model() string { return a.model }

// Endpoint returns the chat-completions URL the auditor will hit (for logging).
func (a *OpenAIFactAuditor) Endpoint() string { return a.apiURL }

// Audit evaluates a single fact row and returns the verdict.
func (a *OpenAIFactAuditor) Audit(ctx context.Context, in FactAuditInput) FactAuditResult {
	prompt := buildFactAuditPrompt(in)

	reqPayload := llm.ChatRequest{
		Model: a.model,
		Messages: []llm.ChatMessage{
			{Role: "user", Content: prompt},
		},
		Temperature: 0.1,
		// The audit response is three short lines (REASONING/RULE/VERDICT), well
		// under 200 tokens in practice. We cap at 1024 to leave generous headroom
		// for unusually long reasoning while still fitting comfortably in an 8k
		// ctx window alongside the verbatim extraction prompt (~3.5k tokens).
		MaxTokens: 1024,
	}
	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return FactAuditResult{Err: fmt.Errorf("marshal audit request: %w", err)}
	}

	bodyBytes, err := a.baseClient.DoRequest(ctx, a.apiURL, a.apiKey, payloadBytes)
	if err != nil {
		return FactAuditResult{Err: fmt.Errorf("audit API call failed: %w", err)}
	}

	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return FactAuditResult{Err: fmt.Errorf("decode audit response: %w (raw: %s)", err, string(bodyBytes))}
	}
	if chatResp.Error != nil {
		return FactAuditResult{Err: fmt.Errorf("audit API error: %s", chatResp.Error.Message)}
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return FactAuditResult{Err: fmt.Errorf("audit API returned no content (raw: %s)", string(bodyBytes))}
	}

	llmText := chatResp.Choices[0].Message.Content
	res := parseFactAuditResponse(llmText)
	res.RawResp = llmText
	return res
}

// buildFactAuditPrompt fills the audit template with (a) the verbatim extraction
// prompt and (b) the formatted fact-under-audit block.
func buildFactAuditPrompt(in FactAuditInput) string {
	// Render the original extraction prompt with audit-mode placeholders for
	// the slots that would normally hold live ingest data. We keep the WHOLE
	// prompt (rules, examples, JSON output spec) so the LLM sees exactly what
	// the extractor sees — nothing redacted.
	extractionPrompt := BuildFactExtractionPrompt(FactExtractorConfig{
		TargetName:         "(audit mode — primary subject not relevant; evaluate the fact below against the rules)",
		SourceURL:          "(audit mode — see source_url in the fact-under-audit block)",
		Content:            "(audit mode — the original article content is not provided; rely on the fact's normalized_fact and original_quote below)",
		PluginInstructions: "",
	})

	auditBlock := formatFactForAudit(in)
	return fmt.Sprintf(auditFactPromptTpl, extractionPrompt, auditBlock)
}

func formatFactForAudit(in FactAuditInput) string {
	var b strings.Builder
	if s := strings.TrimSpace(in.SubjectEntity); s != "" {
		fmt.Fprintf(&b, "- subject_entity:    %s\n", s)
	}
	fmt.Fprintf(&b, "- normalized_fact:   %s\n", strings.TrimSpace(in.NormalizedFact))
	if s := strings.TrimSpace(in.CanonicalVerb); s != "" {
		fmt.Fprintf(&b, "- canonical_verb:    %s\n", s)
	}
	if s := strings.TrimSpace(in.EmbeddingText); s != "" {
		fmt.Fprintf(&b, "- embedding_text:    %s\n", s)
	}
	if s := strings.TrimSpace(in.OriginalQuote); s != "" {
		fmt.Fprintf(&b, "- original_quote:    %q\n", s)
	}
	if s := strings.TrimSpace(in.SourceURL); s != "" {
		fmt.Fprintf(&b, "- source_url:        %s\n", s)
	}
	if in.SourcePublishedAt != nil {
		fmt.Fprintf(&b, "- source_published_at: %s\n", in.SourcePublishedAt.Format("2006-01-02"))
	}
	if in.FactCreatedAt != nil {
		fmt.Fprintf(&b, "- fact_created_at:   %s\n", in.FactCreatedAt.Format("2006-01-02"))
	}
	return strings.TrimRight(b.String(), "\n")
}

// parseFactAuditResponse extracts REASONING / RULE / VERDICT lines.
func parseFactAuditResponse(response string) FactAuditResult {
	var (
		reasoning, rule string
		verdict         FactAuditVerdict
	)
	for _, line := range strings.Split(response, "\n") {
		if m := factAuditReasoningRegex.FindStringSubmatch(line); m != nil && reasoning == "" {
			reasoning = strings.TrimSpace(m[1])
			continue
		}
		if m := factAuditRuleRegex.FindStringSubmatch(line); m != nil && rule == "" {
			rule = strings.TrimSpace(m[1])
			continue
		}
		if m := factAuditVerdictRegex.FindStringSubmatch(line); m != nil && verdict == "" {
			verdict = FactAuditVerdict(strings.ToLower(strings.TrimSpace(m[1])))
		}
	}

	if verdict == "" {
		return FactAuditResult{
			Reasoning: reasoning,
			Rule:      rule,
			Err:       fmt.Errorf("could not parse VERDICT line from audit response: %q", response),
		}
	}
	return FactAuditResult{
		Verdict:   verdict,
		Reasoning: reasoning,
		Rule:      rule,
	}
}
