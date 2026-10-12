package llm

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
)

const jsonCorrectiveTpl = `Your previous reply was rejected: %s.

Reply ONCE MORE with ONLY the JSON object that the first message asks for. Do NOT add commentary, headers, code fences, or any text outside the JSON object.`

// ChatJSON sends req and decodes the reply into T with ParseJSONResponse.
// When the reply does not decode, ChatJSON sends ONE corrective turn: the bad
// reply goes back as the assistant turn, and a user turn quotes the parse
// error. A second bad reply returns an error that quotes the two replies.
// name prefixes the warning of the corrective retry, for example
// "Entity dedup". Field-level validation stays with the caller.
func ChatJSON[T any](ctx context.Context, c *Client, req ChatRequest, name string) (T, error) {
	var zero T
	first, _, err := c.Chat(ctx, req)
	if err != nil {
		return zero, err
	}
	v, err := ParseJSONResponse[T](first)
	if err == nil {
		return v, nil
	}

	log.Warn().Err(err).Str("raw_response", first).
		Msg(name + ": first response is not valid JSON; issuing corrective retry")
	messages := make([]ChatMessage, 0, len(req.Messages)+2)
	messages = append(messages, req.Messages...)
	messages = append(messages,
		ChatMessage{Role: "assistant", Content: first},
		ChatMessage{Role: "user", Content: fmt.Sprintf(jsonCorrectiveTpl, err.Error())},
	)
	req.Messages = messages
	second, _, err := c.Chat(ctx, req)
	if err != nil {
		return zero, fmt.Errorf("JSON corrective retry failed: %w (first reply: %q)", err, first)
	}
	v, err = ParseJSONResponse[T](second)
	if err != nil {
		return zero, fmt.Errorf("LLM returned invalid JSON twice: %w (first reply: %q, second reply: %q)", err, first, second)
	}
	return v, nil
}
