package llm

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/rs/zerolog/log"
)

// Client is a BaseLLMClient bound to one chat-completions endpoint: the full
// URL, the key and the default model. Build one with Config.NewClient or
// NewClientFromEnv.
type Client struct {
	*BaseLLMClient
	URL   string // full chat-completions URL
	Key   string
	Model string
}

// Chat sends one chat-completion request (req.Model defaults to c.Model when
// empty) and returns the first choice's content, untrimmed, together with the
// decoded envelope. The transport, retry, failover, logging and metrics are
// exactly DoRequest's; on top of it Chat fails on an undecodable body, an error
// envelope, and a reply with no choices or empty content. resp is non-nil
// whenever the body decoded, so callers can read usage and finish_reason even
// when the guards fail. When req.Task thinks (task.go), Chat turns thinking
// on with a thinking budget on top of MaxTokens. When req asks for a JSON object, the content comes
// back without a Markdown code fence: some models (Qwen3.8 on llama.cpp)
// wrap the object in ```json even in JSON mode, and a caller must not have to
// know it. The active routing (SetRouting) chooses the endpoint of each call
// from req.Task: on a route that is not local, that endpoint answers the
// call instead, with the Claude settings of the route.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (string, *ChatResponse, error) {
	r := activeRouting()
	if rt := r.Route(req.Task); rt.Endpoint != EndpointLocal {
		return r.Endpoints[rt.Endpoint].Call(ctx, req, rt.claudeOptions())
	}
	if req.Model == "" {
		req.Model = c.Model
	}
	applyThinking(&req)
	payload, err := json.Marshal(req)
	if err != nil {
		return "", nil, fmt.Errorf("marshal chat request: %w", err)
	}
	body, err := c.DoRequest(ctx, c.URL, c.Key, payload)
	if err != nil {
		return "", nil, err
	}
	var resp ChatResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return "", nil, fmt.Errorf("decode chat response: %w (raw: %s)", err, string(body))
	}
	if resp.Error != nil {
		return "", &resp, fmt.Errorf("chat API error: [%s] %s - %s", resp.Error.Code, resp.Error.Type, resp.Error.Message)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		return "", &resp, fmt.Errorf("chat API returned no content (raw: %s)", string(body))
	}
	if resp.Choices[0].FinishReason == "length" {
		// The reply hit MaxTokens: a JSON or verdict line is probably cut.
		// The caller's parse error follows; this names the cause.
		log.Warn().Str("task", string(req.Task)).Int("max_tokens", req.MaxTokens).
			Int("completion_tokens", resp.Usage.CompletionTokens).
			Msg("LLM reply truncated at max_tokens")
	}
	content := resp.Choices[0].Message.Content
	if req.wantsJSON() {
		content = StripJSONCodeFence(content)
		if content == "" {
			return "", &resp, fmt.Errorf("chat API returned an empty code fence (raw: %s)", string(body))
		}
	}
	return content, &resp, nil
}
