package core

import (
	"context"
	"digital-mirror/backend/llm"
	"fmt"
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
	client *llm.Client
}

// NewLLMMetaSynthesizerFromEnv builds a synthesizer from OPEN_AI_ env vars.
func NewLLMMetaSynthesizerFromEnv() (*LLMMetaSynthesizer, error) {
	client, err := llm.NewClientFromEnv("meta-fact-synthesizer")
	if err != nil {
		return nil, err
	}
	return &LLMMetaSynthesizer{client: client}, nil
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

	content, _, err := m.client.Chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature:    0.0,
		MaxTokens:      512,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		return "", fmt.Errorf("meta-synthesis LLM call failed: %w", err)
	}

	out, err := llm.ParseJSONResponse[struct {
		MetaFact string `json:"meta_fact"`
	}](content)
	if err != nil {
		log.Error().Str("llm_content", content).Msg("Failed to parse meta-synthesis response")
		return "", fmt.Errorf("invalid meta-synthesis JSON: %w", err)
	}
	if strings.TrimSpace(out.MetaFact) == "" {
		return "", fmt.Errorf("meta-synthesis returned empty meta_fact")
	}
	return strings.TrimSpace(out.MetaFact), nil
}
