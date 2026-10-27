package llm

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ChatCompletionsURL composes the full chat-completions endpoint from an
// OpenAI-compatible base URL. It is idempotent: a base that already ends in
// "/chat/completions" is returned unchanged, so callers may pass either the
// API root (e.g. "https://api.openai.com/v1") or a fully-qualified endpoint.
func ChatCompletionsURL(base string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/chat/completions") {
		return base
	}
	return base + "/chat/completions"
}

// StripJSONCodeFence removes a Markdown code fence (```json … ``` or ``` … ```)
// that models sometimes wrap JSON output in, returning the inner payload
// trimmed of surrounding whitespace. Input without a leading fence is returned
// trimmed but otherwise unchanged.
func StripJSONCodeFence(s string) string {
	s = strings.TrimSpace(s)
	switch {
	case strings.HasPrefix(s, "```json"):
		s = strings.TrimPrefix(s, "```json")
	case strings.HasPrefix(s, "```"):
		s = strings.TrimPrefix(s, "```")
	default:
		return s
	}
	if idx := strings.LastIndex(s, "```"); idx != -1 {
		s = s[:idx]
	}
	return strings.TrimSpace(s)
}

// ParseJSONResponse decodes an LLM reply into T after StripJSONCodeFence.
// A reply that writes its reasoning as prose before the JSON (the Claude
// models do, fenced or not) is decoded from the first object or array in it
// that decodes into T. Field-level validation stays with the caller.
func ParseJSONResponse[T any](content string) (T, error) {
	var v T
	err := json.Unmarshal([]byte(StripJSONCodeFence(content)), &v)
	if err == nil {
		return v, nil
	}
	for i, c := range content {
		if c != '{' && c != '[' {
			continue
		}
		var w T
		if json.NewDecoder(strings.NewReader(content[i:])).Decode(&w) == nil {
			return w, nil
		}
	}
	return v, fmt.Errorf("invalid JSON response: %w", err)
}
