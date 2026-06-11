package llm

import "strings"

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
