package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type jsonRetryReply struct {
	Reasoning string `json:"reasoning"`
	Same      bool   `json:"same"`
}

// jsonRetryServer answers each call with the next reply of replies (the last
// one repeats) and records the request bodies.
func jsonRetryServer(t *testing.T, replies ...string) (*Client, *[][]byte) {
	t.Helper()
	resetBreaker()
	t.Cleanup(resetBreaker)
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("CF_ACCESS_CLIENT_ID", "")
	var bodies [][]byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, b)
		reply := replies[min(len(bodies), len(replies))-1]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(ChatResponse{Choices: []ChatChoice{{Message: ChatMessage{Role: "assistant", Content: reply}}}})
	}))
	t.Cleanup(srv.Close)
	c := Config{BaseURL: srv.URL, APIKey: "k", ChatModel: "m"}.NewClient("test")
	c.httpClient = &http.Client{Timeout: 2 * time.Second}
	return c, &bodies
}

func jsonRetryRequest() ChatRequest {
	return ChatRequest{
		Messages:       []ChatMessage{{Role: "user", Content: "PROMPT"}},
		Temperature:    0.1,
		MaxTokens:      256,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	}
}

// A prose first reply gets one corrective turn: the bad reply goes back as
// the assistant turn, the parse error is quoted, and the sampling parameters
// stay the same.
func TestChatJSON_CorrectiveRetry(t *testing.T) {
	c, bodies := jsonRetryServer(t, "After a check, the two names are the same.", `{"reasoning": "accent variant", "same": true}`)

	v, err := ChatJSON[jsonRetryReply](context.Background(), c, jsonRetryRequest(), "Test")
	require.NoError(t, err)
	assert.Equal(t, jsonRetryReply{Reasoning: "accent variant", Same: true}, v)
	require.Len(t, *bodies, 2)

	var retry ChatRequest
	require.NoError(t, json.Unmarshal((*bodies)[1], &retry))
	assert.Equal(t, 0.1, retry.Temperature)
	assert.Equal(t, 256, retry.MaxTokens)
	require.NotNil(t, retry.ResponseFormat)
	assert.Equal(t, "json_object", retry.ResponseFormat.Type)
	require.Len(t, retry.Messages, 3)
	assert.Equal(t, ChatMessage{Role: "user", Content: "PROMPT"}, retry.Messages[0])
	assert.Equal(t, ChatMessage{Role: "assistant", Content: "After a check, the two names are the same."}, retry.Messages[1])
	assert.Equal(t, "user", retry.Messages[2].Role)
	assert.True(t, strings.HasPrefix(retry.Messages[2].Content, "Your previous reply was rejected: invalid JSON response: invalid character 'A'"), retry.Messages[2].Content)
	assert.Contains(t, retry.Messages[2].Content, "ONLY the JSON object")
}

// A valid first reply costs one call.
func TestChatJSON_NoRetryOnValidReply(t *testing.T) {
	c, bodies := jsonRetryServer(t, `{"reasoning": "r", "same": false}`)

	v, err := ChatJSON[jsonRetryReply](context.Background(), c, jsonRetryRequest(), "Test")
	require.NoError(t, err)
	assert.Equal(t, jsonRetryReply{Reasoning: "r", Same: false}, v)
	assert.Len(t, *bodies, 1)
}

// A second bad reply returns an error that quotes the two replies.
func TestChatJSON_TwiceInvalid(t *testing.T) {
	c, bodies := jsonRetryServer(t, "first prose", "second prose")

	_, err := ChatJSON[jsonRetryReply](context.Background(), c, jsonRetryRequest(), "Test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid JSON twice")
	assert.Contains(t, err.Error(), `first reply: "first prose"`)
	assert.Contains(t, err.Error(), `second reply: "second prose"`)
	assert.Len(t, *bodies, 2)
}
