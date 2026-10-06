package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// chatTestClient serves one canned body from an httptest server and records
// the request payload. The primary-only path is exercised (no fallback env).
func chatTestClient(t *testing.T, status int, body string) (*Client, *[]byte) {
	t.Helper()
	resetBreaker()
	t.Cleanup(resetBreaker)
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("CF_ACCESS_CLIENT_ID", "")
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	c := Config{BaseURL: srv.URL, APIKey: "k", ChatModel: "cfg-model"}.NewClient("test")
	c.httpClient = &http.Client{Timeout: 2 * time.Second}
	return c, &got
}

func TestChat_Success(t *testing.T) {
	c, got := chatTestClient(t, 200, `{"choices":[{"message":{"role":"assistant","content":"  hello \n"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	content, resp, err := c.Chat(context.Background(), ChatRequest{
		Messages:    []ChatMessage{{Role: "user", Content: "hi"}},
		Temperature: 0.1,
		MaxTokens:   7,
	})
	if err != nil {
		t.Fatal(err)
	}
	if content != "  hello \n" {
		t.Fatalf("content must be returned untrimmed, got %q", content)
	}
	if resp == nil || resp.Usage.CompletionTokens != 2 || resp.Choices[0].FinishReason != "stop" {
		t.Fatalf("envelope not decoded: %+v", resp)
	}
	var sent map[string]any
	if err := json.Unmarshal(*got, &sent); err != nil {
		t.Fatal(err)
	}
	if sent["model"] != "cfg-model" {
		t.Fatalf("empty req.Model must default to the client model, got %v", sent["model"])
	}
	if sent["temperature"] != 0.1 || sent["max_tokens"] != float64(7) {
		t.Fatalf("request parameters not forwarded: %v", sent)
	}
}

func TestChat_ModelOverride(t *testing.T) {
	c, got := chatTestClient(t, 200, `{"choices":[{"message":{"content":"ok"}}]}`)
	if _, _, err := c.Chat(context.Background(), ChatRequest{Model: "other", Messages: []ChatMessage{{Role: "user", Content: "x"}}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(*got), `"model":"other"`) {
		t.Fatalf("explicit req.Model must win, sent %s", *got)
	}
}

func TestChat_ErrorEnvelope(t *testing.T) {
	c, _ := chatTestClient(t, 200, `{"error":{"type":"invalid_request_error","message":"bad prompt","code":"ctx"}}`)
	_, resp, err := c.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}})
	if err == nil || !strings.Contains(err.Error(), "[ctx] invalid_request_error - bad prompt") {
		t.Fatalf("error envelope must surface type/message/code, got %v", err)
	}
	if resp == nil || resp.Error == nil {
		t.Fatal("decoded envelope must be returned alongside the error")
	}
}

func TestChat_EmptyChoices(t *testing.T) {
	c, _ := chatTestClient(t, 200, `{"choices":[]}`)
	if _, _, err := c.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}}); err == nil || !strings.Contains(err.Error(), "no content") {
		t.Fatalf("empty choices must fail, got %v", err)
	}
	c2, _ := chatTestClient(t, 200, `{"choices":[{"message":{"content":""}}]}`)
	if _, _, err := c2.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}}); err == nil || !strings.Contains(err.Error(), "no content") {
		t.Fatalf("empty content must fail, got %v", err)
	}
}

func TestChat_UndecodableBody(t *testing.T) {
	c, _ := chatTestClient(t, 200, `not json`)
	if _, resp, err := c.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}}); err == nil || resp != nil || !strings.Contains(err.Error(), "decode chat response") {
		t.Fatalf("undecodable body must fail with nil envelope, got %v %v", resp, err)
	}
}

func TestChat_TransportErrorIsClientError(t *testing.T) {
	c, _ := chatTestClient(t, 400, `{"error":{"message":"maximum context length exceeded"}}`)
	_, _, err := c.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}})
	var ce *ClientError
	if err == nil || !errors.As(err, &ce) || !strings.Contains(err.Error(), "maximum context length") {
		t.Fatalf("a 4xx must surface DoRequest's ClientError with the body, got %v", err)
	}
}

// In JSON mode the content comes back without the Markdown code fence some
// models add around the object; in text mode it is untouched.
func TestChat_JSONModeStripsCodeFence(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"` + "```json\\n{\\\"a\\\": 1}\\n```" + `"},"finish_reason":"stop"}]}`
	c, _ := chatTestClient(t, 200, body)
	content, _, err := c.Chat(context.Background(), ChatRequest{
		Messages:       []ChatMessage{{Role: "user", Content: "hi"}},
		ResponseFormat: &ResponseFormat{Type: "json_object"},
	})
	if err != nil || content != `{"a": 1}` {
		t.Fatalf("JSON mode: content %q, err %v; want the bare object", content, err)
	}

	c, _ = chatTestClient(t, 200, body)
	content, _, err = c.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}})
	if err != nil || !strings.HasPrefix(content, "```json") {
		t.Fatalf("text mode: content %q, err %v; want it untouched", content, err)
	}
}
