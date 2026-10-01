// Package translate is the extensible, provider-agnostic translation layer. A
// single Translator interface turns text from one language into another; the
// async translate phase (dm translate run) uses it to fill the generic
// translations table for every configured target language. Adding a new language
// is config-only; adding a new provider is one Translator implementation.
package translate

import (
	"context"
	"crypto/sha256"
	"digital-mirror/backend/llm"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// DefaultTargetLangs is the built-in set of languages every translatable entity is
// translated into. Kept as a constant for now; a TRANSLATION_TARGET_LANGS env var
// may override it later (pending explicit approval — no new env var without it).
var DefaultTargetLangs = []string{"en", "fr"}

// Translator produces a faithful translation of text between two languages. The
// only method a new backend (another provider, a local model, a glossary engine)
// must implement.
type Translator interface {
	Translate(ctx context.Context, text, srcLang, dstLang string) (string, error)
}

// SourceHash returns a stable hash of source text. The translate phase stores it
// alongside each translation so it can detect staleness (source changed -> the
// translation must be regenerated) without comparing full text.
func SourceHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// LangsToFill returns the target languages that still need a translation for one
// piece of text: every target except the source language and any already present.
// Language codes are compared case-insensitively. Order follows targets.
func LangsToFill(targets []string, srcLang string, existing map[string]bool) []string {
	src := normLang(srcLang)
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		lt := normLang(t)
		if lt == "" || lt == src || existing[lt] {
			continue
		}
		out = append(out, lt)
	}
	return out
}

// FillMissing translates text into every target language that LangsToFill selects,
// returning lang->translation. Best-effort: a per-language failure is logged and
// skipped so one bad language never blocks the others (this runs in the async
// phase, decoupled from the rest of the pipeline). When the LLM is unavailable
// it stops and returns the error (wrapping llm.ErrUnavailable) with the
// translations made so far; so it does when ctx is cancelled, with ctx.Err().
func FillMissing(ctx context.Context, tr Translator, text, srcLang string, targets []string, existing map[string]bool) (map[string]string, error) {
	out := make(map[string]string)
	for _, lang := range LangsToFill(targets, srcLang, existing) {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		translated, err := tr.Translate(ctx, text, srcLang, lang)
		if errors.Is(err, llm.ErrUnavailable) {
			return out, err
		}
		if err != nil && ctx.Err() != nil {
			return out, ctx.Err()
		}
		if err != nil {
			log.Warn().Err(err).Str("src", srcLang).Str("dst", lang).Msg("translation failed; skipping this language")
			continue
		}
		out[lang] = translated
	}
	return out, nil
}

func normLang(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// langName maps a BCP-47 code to an English language name for the prompt.
func langName(code string) string {
	switch normLang(code) {
	case "en":
		return "English"
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
		// "auto"/"" or any unknown code: let the model detect the source language.
		// Only ever hit for the SOURCE side; target langs always come from config.
		return "the source language"
	}
}

// OpenAITranslator is the live, OpenAI-compatible Translator. It handles any
// language pair via a single faithful-translation prompt at temperature 0.
type OpenAITranslator struct {
	client *llm.Client
}

// NewOpenAITranslatorFromEnv builds an OpenAITranslator from OPEN_AI_ env vars.
func NewOpenAITranslatorFromEnv() (*OpenAITranslator, error) {
	client, err := llm.NewClientFromEnv("translate")
	if err != nil {
		return nil, err
	}
	return &OpenAITranslator{client: client}, nil
}

type translationResult struct {
	Translation string `json:"translation"`
}

// Translate returns a faithful translation of text from srcLang to dstLang.
func (t *OpenAITranslator) Translate(ctx context.Context, text, srcLang, dstLang string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("translate: empty source text")
	}
	prompt := fmt.Sprintf(`Translate the following political text from %s to %s. Preserve EVERY concrete detail faithfully — numbers, dates, named people/parties/bodies, negations, conditionals, quotes. Do NOT paraphrase, summarize, editorialize, or add anything.

TEXT: "%s"

Return ONLY strict JSON, nothing else:
{"translation": "<the %s translation>"}`, langName(srcLang), langName(dstLang), text, langName(dstLang))

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
		return "", fmt.Errorf("invalid translate JSON: %w", err)
	}
	out := strings.TrimSpace(res.Translation)
	if out == "" {
		return "", fmt.Errorf("translate returned empty result")
	}
	return out, nil
}
