package core

import (
	"context"
	"digital-mirror/backend/llm"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
)

// MetaSynthesizer produces the aggregated text of a meta-fact from its member
// facts. Called when a meta is created (2 members) and every time it absorbs
// another member.
type MetaSynthesizer interface {
	SynthesizeMetaFact(ctx context.Context, subject string, memberTexts []string, lang string) (string, error)
}

// LLMMetaSynthesizer implements MetaSynthesizer against the OPEN_AI_* endpoint.
type LLMMetaSynthesizer struct {
	apiKey     string
	apiURL     string
	model      string
	baseClient *llm.BaseLLMClient
}

// NewLLMMetaSynthesizerFromEnv builds a synthesizer from OPEN_AI_ env vars.
func NewLLMMetaSynthesizerFromEnv() (*LLMMetaSynthesizer, error) {
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
	return &LLMMetaSynthesizer{
		apiKey:     apiKey,
		apiURL:     llm.ChatCompletionsURL(strings.TrimRight(base, "/")),
		model:      model,
		baseClient: llm.NewBaseLLMClient("meta-fact-synthesizer"),
	}, nil
}

// maxSynthesisMembers caps how many member texts go into the prompt; members
// beyond the cap are already represented (they folded because they state the
// same claim), so the newest ones suffice.
const maxSynthesisMembers = 10

func (m *LLMMetaSynthesizer) SynthesizeMetaFact(ctx context.Context, subject string, memberTexts []string, lang string) (string, error) {
	if len(memberTexts) == 0 {
		return "", fmt.Errorf("no member texts to synthesize")
	}
	texts := memberTexts
	if len(texts) > maxSynthesisMembers {
		texts = texts[len(texts)-maxSynthesisMembers:]
	}
	var sb strings.Builder
	for i, t := range texts {
		fmt.Fprintf(&sb, "%d. %q\n", i+1, t)
	}

	prompt := fmt.Sprintf(`The following statements were all recorded about the SAME person (%s) and all express the SAME claim in different words. Write ONE aggregated statement that represents them.

Rules:
- Same language as the statements (%s).
- Keep every load-bearing specific that ALL statements share (the position taken, its direction, the subject matter). Drop incidental per-article details (outlet, phrasing, minor dates).
- Do NOT generalize beyond what the statements say, do NOT add information, do NOT soften or strengthen the position.
- One sentence, third person, starting with the person's name.

STATEMENTS:
%s
Return ONLY strict JSON, nothing else:
{"meta_fact": "<the aggregated statement>"}`, subject, lang, sb.String())

	reqPayload := llm.ChatRequest{
		Model:          m.model,
		Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature:    0.0,
		MaxTokens:      512,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("marshal meta-synthesis request: %w", err)
	}
	bodyBytes, err := m.baseClient.DoRequest(ctx, m.apiURL, m.apiKey, payloadBytes)
	if err != nil {
		return "", fmt.Errorf("meta-synthesis LLM call failed: %w", err)
	}
	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		return "", fmt.Errorf("decode meta-synthesis response: %w", err)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("meta-synthesis LLM returned no content")
	}

	var out struct {
		MetaFact string `json:"meta_fact"`
	}
	content := llm.StripJSONCodeFence(strings.TrimSpace(chatResp.Choices[0].Message.Content))
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		log.Error().Str("llm_content", content).Msg("Failed to parse meta-synthesis response")
		return "", fmt.Errorf("invalid meta-synthesis JSON: %w", err)
	}
	if strings.TrimSpace(out.MetaFact) == "" {
		return "", fmt.Errorf("meta-synthesis returned empty meta_fact")
	}
	return strings.TrimSpace(out.MetaFact), nil
}
