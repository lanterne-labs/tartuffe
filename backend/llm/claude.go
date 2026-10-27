package llm

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/rs/zerolog/log"
)

// The Claude endpoint is the native Messages API
// (https://platform.claude.com/docs/en/api/messages). The OpenAI-compatible
// endpoint of Anthropic is not used: it has no prompt cache, it ignores
// response_format and it does not return the thinking
// (https://platform.claude.com/docs/en/cli-sdks-libraries/libraries/openai-sdk).
const (
	claudeBaseURL    = "https://api.anthropic.com/v1"
	claudeAPIVersion = "2023-06-01"
)

// Effort is the output_config.effort of a Claude call. On the 5.5 models the
// thinking is adaptive and effort is its only control: budget_tokens returns
// a 400, and Opus 5.5 and Sonnet 5.5 cannot turn the thinking off
// (https://platform.claude.com/docs/en/build-with-claude/effort).
type Effort string

const (
	EffortLow    Effort = "low"
	EffortMedium Effort = "medium"
	EffortHigh   Effort = "high"
)

// ClaudeOptions are the settings of a Claude call that ChatRequest does not
// carry.
type ClaudeOptions struct {
	Model  string
	Effort Effort
	// ThinkingTokens is added to ChatRequest.MaxTokens: on Claude, max_tokens
	// caps the thinking and the answer together.
	ThinkingTokens int
}

type claudeCacheControl struct {
	Type string `json:"type"`
	TTL  string `json:"ttl,omitempty"`
}

type claudeBlock struct {
	Type         string              `json:"type"`
	Text         string              `json:"text"`
	CacheControl *claudeCacheControl `json:"cache_control,omitempty"`
}

type claudeMessage struct {
	Role    string        `json:"role"`
	Content []claudeBlock `json:"content"`
}

type claudeThinking struct {
	Type string `json:"type"`
}

type claudeOutputConfig struct {
	Effort Effort `json:"effort,omitempty"`
}

// ClaudeRequest is the body of POST /v1/messages, and the params of one
// request of a message batch.
type ClaudeRequest struct {
	Model        string              `json:"model"`
	MaxTokens    int                 `json:"max_tokens"`
	System       []claudeBlock       `json:"system,omitempty"`
	Messages     []claudeMessage     `json:"messages"`
	Thinking     *claudeThinking     `json:"thinking,omitempty"`
	OutputConfig *claudeOutputConfig `json:"output_config,omitempty"`
}

// NewClaudeRequest converts an OpenAI-style request to a Claude request.
//
// The conversion drops three fields on purpose:
//   - Temperature: a value other than the default returns a 400 on the 5.5
//     models (https://platform.claude.com/docs/en/build-with-claude/thinking).
//     The votes of a judge sample at the default temperature.
//   - ResponseFormat: Claude has no JSON mode without a schema. The prompts
//     ask for JSON, and ClaudeReply.Text keeps the first JSON value of the
//     reply (claudeAnswer).
//   - The thinking fields of the local server (ChatTemplateKwargs,
//     ThinkingBudgetTokens): Claude thinks adaptively at opts.Effort.
func NewClaudeRequest(req ChatRequest, opts ClaudeOptions) (ClaudeRequest, error) {
	if opts.Model == "" {
		return ClaudeRequest{}, errors.New("claude request: no model")
	}
	if req.MaxTokens <= 0 {
		return ClaudeRequest{}, errors.New("claude request: MaxTokens must be set")
	}
	out := ClaudeRequest{
		Model:     opts.Model,
		MaxTokens: req.MaxTokens + opts.ThinkingTokens,
		Thinking:  &claudeThinking{Type: "adaptive"},
	}
	if opts.Effort != "" {
		out.OutputConfig = &claudeOutputConfig{Effort: opts.Effort}
	}
	for i, m := range req.Messages {
		blocks := []claudeBlock{{Type: "text", Text: m.Content}}
		switch m.Role {
		case "system":
			if len(out.Messages) > 0 {
				return ClaudeRequest{}, fmt.Errorf("claude request: system message %d after a user message", i)
			}
			out.System = append(out.System, blocks...)
		case "user", "assistant":
			out.Messages = append(out.Messages, claudeMessage{Role: m.Role, Content: blocks})
		default:
			return ClaudeRequest{}, fmt.Errorf("claude request: message %d: unknown role %q", i, m.Role)
		}
	}
	// Claude needs a user message. A prompt sent as one system message (the
	// fact extractor) goes as the user message, with the same text.
	if len(out.Messages) == 0 && len(out.System) == 1 {
		out.Messages = []claudeMessage{{Role: "user", Content: out.System}}
		out.System = nil
	}
	if len(out.Messages) == 0 {
		return ClaudeRequest{}, errors.New("claude request: no user message")
	}
	return out, nil
}

// firstUserText returns the index and the text of the first user message.
// NewClaudeRequest gives every message one text block.
func (r ClaudeRequest) firstUserText() (int, string, bool) {
	for i, m := range r.Messages {
		if m.Role == "user" && len(m.Content) == 1 {
			return i, m.Content[0].Text, true
		}
	}
	return 0, "", false
}

// withCacheBreakpoint returns a copy of r with the first user message split
// at offset: the part before it becomes a block with a cache breakpoint. The
// cache then holds everything up to the breakpoint, the system blocks included
// (https://platform.claude.com/docs/en/build-with-claude/prompt-caching).
// ttl is "" (5 min) or "1h".
func (r ClaudeRequest) withCacheBreakpoint(offset int, ttl string) (ClaudeRequest, error) {
	i, text, ok := r.firstUserText()
	if !ok {
		return ClaudeRequest{}, errors.New("cache breakpoint: no single-block user message")
	}
	if offset <= 0 || offset > len(text) {
		return ClaudeRequest{}, fmt.Errorf("cache breakpoint %d outside the text (%d bytes)", offset, len(text))
	}
	blocks := []claudeBlock{{Type: "text", Text: text[:offset], CacheControl: &claudeCacheControl{Type: "ephemeral", TTL: ttl}}}
	if rest := text[offset:]; rest != "" {
		blocks = append(blocks, claudeBlock{Type: "text", Text: rest})
	}
	out := r
	out.Messages = append([]claudeMessage(nil), r.Messages...)
	out.Messages[i] = claudeMessage{Role: "user", Content: blocks}
	return out, nil
}

// canonical returns r without cache breakpoints: the text blocks of each
// message joined, no cache_control. A breakpoint does not change the answer,
// so the identity of a request (claudeCustomID) uses this form.
func (r ClaudeRequest) canonical() ClaudeRequest {
	join := func(blocks []claudeBlock) []claudeBlock {
		return []claudeBlock{{Type: "text", Text: joinBlocks(blocks)}}
	}
	out := r
	if len(r.System) > 0 {
		out.System = join(r.System)
	}
	out.Messages = make([]claudeMessage, len(r.Messages))
	for i, m := range r.Messages {
		out.Messages[i] = claudeMessage{Role: m.Role, Content: join(m.Content)}
	}
	return out
}

// joinBlocks returns the text of the blocks, with no separator.
func joinBlocks(blocks []claudeBlock) string {
	var b strings.Builder
	for _, x := range blocks {
		b.WriteString(x.Text)
	}
	return b.String()
}

// hasCacheBreakpoint reports whether r caches a prefix.
func (r ClaudeRequest) hasCacheBreakpoint() bool {
	for _, b := range r.System {
		if b.CacheControl != nil {
			return true
		}
	}
	for _, m := range r.Messages {
		for _, b := range m.Content {
			if b.CacheControl != nil {
				return true
			}
		}
	}
	return false
}

// ClaudeUsage is the usage block of a Claude reply. InputTokens excludes the
// tokens read from or written to the cache.
type ClaudeUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	CacheCreation            struct {
		Ephemeral5m int `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
	// OutputTokensDetails.ThinkingTokens is the part of OutputTokens spent on
	// thinking.
	OutputTokensDetails struct {
		ThinkingTokens int `json:"thinking_tokens"`
	} `json:"output_tokens_details"`
}

// Add returns the sum of two usage blocks.
func (u ClaudeUsage) Add(o ClaudeUsage) ClaudeUsage {
	u.InputTokens += o.InputTokens
	u.OutputTokens += o.OutputTokens
	u.CacheCreationInputTokens += o.CacheCreationInputTokens
	u.CacheReadInputTokens += o.CacheReadInputTokens
	u.CacheCreation.Ephemeral5m += o.CacheCreation.Ephemeral5m
	u.CacheCreation.Ephemeral1h += o.CacheCreation.Ephemeral1h
	u.OutputTokensDetails.ThinkingTokens += o.OutputTokensDetails.ThinkingTokens
	return u
}

// ClaudeReply is a decoded Claude message.
type ClaudeReply struct {
	ID         string `json:"id"`
	Model      string `json:"model"`
	StopReason string `json:"stop_reason"`
	// StopDetails is set on a refusal: the category of the safety classifier
	// ("reasoning_extraction", "general_harms", ...). It can be null.
	StopDetails *struct {
		Category    string `json:"category"`
		Explanation string `json:"explanation"`
	} `json:"stop_details,omitempty"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Usage ClaudeUsage `json:"usage"`
}

// Text returns the answer: the text blocks joined, without the thinking
// blocks. It fails when the model stopped before it finished the answer
// (max_tokens), refused, or wrote no text. A reply meant to be JSON comes back
// without a Markdown code fence, as in Chat.
func (r ClaudeReply) Text(wantJSON bool) (string, error) {
	var b strings.Builder
	for _, c := range r.Content {
		if c.Type == "text" {
			b.WriteString(c.Text)
		}
	}
	source := "claude reply " + r.ID
	if r.StopReason == "refusal" && r.StopDetails != nil {
		source += fmt.Sprintf(" (refusal category %q: %s)", r.StopDetails.Category, r.StopDetails.Explanation)
	}
	return claudeAnswer(source, r.StopReason, b.String(), r.Usage.OutputTokens, wantJSON)
}

// claudeAnswer checks the stop reason of a Claude answer and returns its
// text. source names the reply in the errors. The Messages API, the Message
// Batches API and the claude CLI (ClaudeTeamEndpoint) share it.
//
// A JSON answer loses its code fence and keeps one JSON value (firstJSONValue).
// Without a JSON mode, Claude can write a note after the object ("Correction:
// the index is 1, so the assignment is correct."), or a corrected object, and
// the parsers of the callers reject text after the value. A warning logs the
// dropped text. An
// answer that does not start with a JSON value stays unchanged: the parser of
// the caller rejects it.
func claudeAnswer(source, stopReason, text string, outputTokens int, wantJSON bool) (string, error) {
	switch stopReason {
	case "end_turn", "stop_sequence":
	case "max_tokens":
		return "", fmt.Errorf("%s: stopped at max_tokens (%d output tokens)", source, outputTokens)
	default:
		return "", fmt.Errorf("%s: stop_reason %q", source, stopReason)
	}
	if wantJSON {
		text = firstJSONValue(source, StripJSONCodeFence(text))
	}
	if strings.TrimSpace(text) == "" {
		return "", fmt.Errorf("%s: no text", source)
	}
	return text, nil
}

// firstJSONValue returns the JSON value of text that Claude settled on, and
// logs the text it drops. That is the first value, unless the text after it
// holds another value of the same kind: Claude then corrected itself
// ("Correction, to stay strictly faithful: {...}") and the last one is its
// answer. Text that does not start with a JSON value comes back unchanged.
func firstJSONValue(source, text string) string {
	dec := json.NewDecoder(strings.NewReader(text))
	var v json.RawMessage
	if err := dec.Decode(&v); err != nil {
		return text
	}
	rest := strings.TrimSpace(text[dec.InputOffset():])
	if rest == "" {
		return text
	}
	msg := "Claude wrote text after the JSON answer; the answer keeps the JSON value only"
	if last, ok := lastJSONValue(rest, v[0]); ok {
		v = last
		msg = "Claude corrected its JSON answer; the answer keeps the last JSON value"
	}
	note := rest
	if len(note) > 300 {
		note = note[:300] + "..."
	}
	log.Warn().Str("source", source).Int("dropped_bytes", len(rest)).Str("dropped", note).Msg(msg)
	return string(v)
}

// lastJSONValue returns the last JSON value in text that opens with open ('{'
// or '['), the kind of the first answer.
func lastJSONValue(text string, open byte) (json.RawMessage, bool) {
	var last json.RawMessage
	for i := 0; i < len(text); i++ {
		if text[i] != open {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(text[i:]))
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			continue
		}
		last = v
		i += int(dec.InputOffset()) - 1
	}
	return last, last != nil
}

// claudeChatResponse returns the ChatResponse of a Claude answer. The prompt
// tokens include the tokens read from and written to the cache.
func claudeChatResponse(text string, u ClaudeUsage) *ChatResponse {
	resp := &ChatResponse{
		Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: text}, FinishReason: "stop"}},
		Usage: ChatUsage{
			PromptTokens:     u.InputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens,
			CompletionTokens: u.OutputTokens,
		},
	}
	resp.Usage.TotalTokens = resp.Usage.PromptTokens + resp.Usage.CompletionTokens
	resp.Usage.PromptTokensDetails.CachedTokens = u.CacheReadInputTokens
	return resp
}

// decodeClaudeReply decodes the body of a Claude message.
func decodeClaudeReply(body []byte) (ClaudeReply, error) {
	var r ClaudeReply
	if err := json.Unmarshal(body, &r); err != nil {
		return ClaudeReply{}, fmt.Errorf("decode claude reply: %w", err)
	}
	return r, nil
}
