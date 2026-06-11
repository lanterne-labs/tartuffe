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

// Translator produces faithful English translations of non-English fact text.
// It is the repair path for facts whose extraction-time `normalized_fact_en`
// came back empty (the local model occasionally drops the field): a cheap,
// translate-only LLM call that is far more reliable than re-running the full
// extraction. Used by the `dm facts backfill-translations` command and the
// daily-pipeline translation-repair step.
type Translator struct {
	apiKey     string
	apiURL     string
	model      string
	baseClient *llm.BaseLLMClient
}

// NewTranslatorFromEnv builds a Translator from OPEN_AI_ env vars.
func NewTranslatorFromEnv() (*Translator, error) {
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
	return &Translator{
		apiKey:     apiKey,
		apiURL:     llm.ChatCompletionsURL(base),
		model:      model,
		baseClient: llm.NewBaseLLMClient("translate"),
	}, nil
}

type translationResult struct {
	English string `json:"english"`
}

// TranslateToEnglish returns a faithful English translation of text (a political
// statement in srcLang). Returns an error if the model yields no usable English.
func (t *Translator) TranslateToEnglish(ctx context.Context, text, srcLang string) (string, error) {
	prompt := fmt.Sprintf(`Translate the following political statement from %s to English. Preserve EVERY concrete detail faithfully — numbers, dates, named people/parties, negations, conditionals, quotes. Do NOT paraphrase, summarize, editorialize, or add anything.

STATEMENT: "%s"

Return ONLY strict JSON, nothing else:
{"english": "<the English translation>"}`, langName(srcLang), text)

	reqPayload := llm.ChatRequest{
		Model:          t.model,
		Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature:    0.0,
		MaxTokens:      1024,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	}
	payloadBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return "", fmt.Errorf("failed to marshal translate request: %w", err)
	}
	bodyBytes, err := t.baseClient.DoRequest(ctx, t.apiURL, t.apiKey, payloadBytes)
	if err != nil {
		return "", fmt.Errorf("translate LLM call failed: %w", err)
	}
	var chatResp llm.ChatResponse
	if err := json.Unmarshal(bodyBytes, &chatResp); err != nil {
		log.Error().Err(err).Str("raw_response", string(bodyBytes)).Msg("Failed to decode translate chat response")
		return "", fmt.Errorf("failed to decode chat response: %w", err)
	}
	if len(chatResp.Choices) == 0 || chatResp.Choices[0].Message.Content == "" {
		return "", fmt.Errorf("translate LLM returned no content")
	}

	content := llm.StripJSONCodeFence(chatResp.Choices[0].Message.Content)
	var res translationResult
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &res); err != nil {
		return "", fmt.Errorf("invalid translate JSON response: %w", err)
	}
	english := strings.TrimSpace(res.English)
	if english == "" {
		return "", fmt.Errorf("translate returned empty english")
	}
	return english, nil
}

func langName(code string) string {
	switch strings.ToLower(strings.TrimSpace(code)) {
	case "fr":
		return "French"
	case "de":
		return "German"
	case "es":
		return "Spanish"
	case "it":
		return "Italian"
	case "pt":
		return "Portuguese"
	case "nl":
		return "Dutch"
	default:
		return "the source language"
	}
}
