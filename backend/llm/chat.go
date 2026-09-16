package llm

import (
	"context"
	"encoding/json"
	"fmt"
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
// when the guards fail.
func (c *Client) Chat(ctx context.Context, req ChatRequest) (string, *ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.Model
	}
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
	return resp.Choices[0].Message.Content, &resp, nil
}
