package core

import (
	"context"
	"digital-mirror/backend/llm"
	"fmt"
	"strings"
)

// Translator produces faithful English translations of non-English fact text.
// It is the repair path for facts whose extraction-time `normalized_fact_en`
// came back empty (the local model occasionally drops the field): a cheap,
// translate-only LLM call that is far more reliable than re-running the full
// extraction. Used by the `dm facts backfill-translations` command and the
// daily-pipeline translation-repair step.
type Translator struct {
	client *llm.Client
}

// NewTranslatorFromEnv builds a Translator from OPEN_AI_ env vars.
func NewTranslatorFromEnv() (*Translator, error) {
	client, err := llm.NewClientFromEnv("translate")
	if err != nil {
		return nil, err
	}
	return &Translator{client: client}, nil
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

	content, _, err := t.client.Chat(ctx, llm.ChatRequest{
		Messages:       []llm.ChatMessage{{Role: "user", Content: prompt}},
		Temperature:    0.0,
		MaxTokens:      1024,
		ResponseFormat: &llm.ResponseFormat{Type: "json_object"},
	})
	if err != nil {
		return "", fmt.Errorf("translate LLM call failed: %w", err)
	}

	res, err := llm.ParseJSONResponse[translationResult](content)
	if err != nil {
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
