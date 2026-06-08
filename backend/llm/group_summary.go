package llm

import (
	"context"
	"digital-mirror/backend/promptrules"
	"encoding/json"
	"fmt"
	"strings"
)

// groupSummaryMaxTokens caps the merged-summary response. The reply is one short
// sentence plus its translation in a small JSON object; 256 is ample.
const groupSummaryMaxTokens = 256

// GroupSpoke is one contradicting fact within a group, with the reason it
// conflicts with the base claim.
type GroupSpoke struct {
	FactText string
	Reason   string
}

// groupSummaryResponse is the JSON the model returns.
type groupSummaryResponse struct {
	Summary   string `json:"summary"`
	SummaryEn string `json:"summary_en"`
}

// SynthesizeContradictionGroupSummary produces a single merged "one inconsistency"
// sentence (native + English) for a contradiction group: one base claim contradicted
// by N facts. It is the single source of truth for the group-summary LLM call, used
// by the regroup pass. A non-nil error is an operational failure (LLM unreachable /
// unparseable) — callers should log it and leave the existing summary untouched
// rather than wiping it.
func SynthesizeContradictionGroupSummary(ctx context.Context, client *BaseLLMClient, apiURL, apiKey, model, baseFact string, spokes []GroupSpoke) (summary, summaryEn string, err error) {
	if len(spokes) == 0 {
		return "", "", fmt.Errorf("group summary requested with no contradicting facts")
	}

	var b strings.Builder
	for i, s := range spokes {
		fmt.Fprintf(&b, "%d. \"%s\"", i+1, s.FactText)
		if strings.TrimSpace(s.Reason) != "" {
			fmt.Fprintf(&b, " — conflict: %s", s.Reason)
		}
		b.WriteString("\n")
	}

	prompt := fmt.Sprintf(promptrules.GroupSummaryPrompt, baseFact, b.String())

	reqPayload := ChatRequest{
		Model:       model,
		Messages:    []ChatMessage{{Role: "user", Content: prompt}},
		Temperature: 0.0,
		MaxTokens:   groupSummaryMaxTokens,
	}
	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", "", fmt.Errorf("marshal group summary request: %w", err)
	}

	bodyBytes, err := client.DoRequest(ctx, apiURL, apiKey, payloadBytes)
	if err != nil {
		return "", "", fmt.Errorf("group summary API call failed: %w", err)
	}

	var chatResp ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return "", "", fmt.Errorf("decode group summary response: %w", err)
	}
	if len(chatResp.Choices) == 0 || strings.TrimSpace(chatResp.Choices[0].Message.Content) == "" {
		return "", "", fmt.Errorf("group summary response had no content")
	}

	content := strings.TrimSpace(chatResp.Choices[0].Message.Content)
	jsonStr := extractJSONObject(content)
	if jsonStr == "" {
		return "", "", fmt.Errorf("group summary response had no JSON object: %q", content)
	}

	var parsed groupSummaryResponse
	if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
		return "", "", fmt.Errorf("parse group summary JSON %q: %w", jsonStr, err)
	}
	if strings.TrimSpace(parsed.Summary) == "" {
		return "", "", fmt.Errorf("group summary response had empty summary: %q", content)
	}
	return strings.TrimSpace(parsed.Summary), strings.TrimSpace(parsed.SummaryEn), nil
}

// extractJSONObject returns the substring from the first '{' to the last '}'
// inclusive, tolerating models that wrap JSON in prose or markdown fences.
func extractJSONObject(s string) string {
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start == -1 || end == -1 || end < start {
		return ""
	}
	return s[start : end+1]
}
