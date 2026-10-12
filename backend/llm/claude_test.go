package llm

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

func TestNewClaudeRequest(t *testing.T) {
	req := ChatRequest{
		Temperature:    0.3,
		MaxTokens:      512,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
		Messages: []ChatMessage{
			{Role: "system", Content: "You are a judge."},
			{Role: "user", Content: "RULES: answer in JSON.\nDATA: fact 1"},
		},
	}
	got, err := NewClaudeRequest(req, ClaudeOptions{Model: "claude-haiku-5-5", Effort: EffortLow, ThinkingTokens: 1000})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(got)
	want := `{"model":"claude-haiku-5-5","max_tokens":1512,` +
		`"system":[{"type":"text","text":"You are a judge."}],` +
		`"messages":[{"role":"user","content":[{"type":"text","text":"RULES: answer in JSON.\nDATA: fact 1"}]}],` +
		`"thinking":{"type":"adaptive"},"output_config":{"effort":"low"}}`
	if string(body) != want {
		t.Fatalf("claude request\n got %s\nwant %s", body, want)
	}

	cached, err := got.withCacheBreakpoint(len("RULES: answer in JSON.\n"), "1h")
	if err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(cached.Messages)
	want = `[{"role":"user","content":[` +
		`{"type":"text","text":"RULES: answer in JSON.\n","cache_control":{"type":"ephemeral","ttl":"1h"}},` +
		`{"type":"text","text":"DATA: fact 1"}]}]`
	if string(body) != want {
		t.Fatalf("breakpoint\n got %s\nwant %s", body, want)
	}
	if got.hasCacheBreakpoint() || !cached.hasCacheBreakpoint() {
		t.Fatal("withCacheBreakpoint must not change the original request")
	}
	a, _ := json.Marshal(got.canonical())
	b, _ := json.Marshal(cached.canonical())
	if string(a) != string(b) {
		t.Fatalf("a breakpoint must not change the canonical request:\n%s\n%s", a, b)
	}
	if _, err := got.withCacheBreakpoint(1000, ""); err == nil {
		t.Fatal("a breakpoint past the text must fail")
	}
}

// The fact extractor sends its whole prompt as one system message: Claude
// gets it as the user message, with the same text.
func TestNewClaudeRequest_LoneSystemPromptIsTheUserMessage(t *testing.T) {
	got, err := NewClaudeRequest(ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "system", Content: "rules and article"}}}, ClaudeOptions{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.System) != 0 || len(got.Messages) != 1 || got.Messages[0].Role != "user" || got.Messages[0].Content[0].Text != "rules and article" {
		t.Fatalf("got %+v", got)
	}
}

func TestNewClaudeRequest_Rejects(t *testing.T) {
	user := ChatMessage{Role: "user", Content: "x"}
	cases := map[string]struct {
		req  ChatRequest
		opts ClaudeOptions
	}{
		"no model":          {ChatRequest{MaxTokens: 1, Messages: []ChatMessage{user}}, ClaudeOptions{}},
		"no max tokens":     {ChatRequest{Messages: []ChatMessage{user}}, ClaudeOptions{Model: "m"}},
		"no user message":   {ChatRequest{MaxTokens: 1, Messages: []ChatMessage{{Role: "system", Content: "s"}, {Role: "system", Content: "t"}}}, ClaudeOptions{Model: "m"}},
		"unknown role":      {ChatRequest{MaxTokens: 1, Messages: []ChatMessage{{Role: "tool", Content: "s"}}}, ClaudeOptions{Model: "m"}},
		"system after user": {ChatRequest{MaxTokens: 1, Messages: []ChatMessage{user, {Role: "system", Content: "s"}}}, ClaudeOptions{Model: "m"}},
	}
	for name, c := range cases {
		if _, err := NewClaudeRequest(c.req, c.opts); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestClaudeReply_Text(t *testing.T) {
	reply := func(stop string, blocks ...string) ClaudeReply {
		var r ClaudeReply
		r.ID, r.StopReason = "msg_1", stop
		for i := 0; i+1 < len(blocks); i += 2 {
			r.Content = append(r.Content, struct {
				Type string `json:"type"`
				Text string `json:"text"`
			}{blocks[i], blocks[i+1]})
		}
		return r
	}
	got, err := reply("end_turn", "thinking", "", "text", "```json\n{\"a\":1}\n```").Text(true)
	if err != nil || got != `{"a":1}` {
		t.Fatalf("text = %q, %v; want the JSON without the fence and the thinking", got, err)
	}
	if got, err := reply("end_turn", "text", "  plain ").Text(false); err != nil || got != "  plain " {
		t.Fatalf("text = %q, %v; want the text untouched", got, err)
	}
	for _, r := range []ClaudeReply{
		reply("max_tokens", "text", "{\"a\":"),
		reply("refusal", "text", "no"),
		reply("model_context_window_exceeded", "text", "x"),
		reply("end_turn", "thinking", ""),
	} {
		if _, err := r.Text(true); err == nil {
			t.Errorf("stop %q with %d blocks: want an error", r.StopReason, len(r.Content))
		}
	}
	refused, err := decodeClaudeReply([]byte(`{"id":"msg_2","stop_reason":"refusal",` +
		`"stop_details":{"type":"refusal","category":"reasoning_extraction","explanation":"why"},"content":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := refused.Text(true); err == nil || !strings.Contains(err.Error(), `refusal category "reasoning_extraction": why`) {
		t.Fatalf("refusal error = %v; want the category and the explanation", err)
	}
}

func TestClaudeCostUSD(t *testing.T) {
	var u ClaudeUsage
	u.InputTokens = 1_000_000
	u.OutputTokens = 1_000_000
	u.CacheReadInputTokens = 1_000_000
	u.CacheCreationInputTokens = 2_000_000
	u.CacheCreation.Ephemeral5m = 1_000_000
	u.CacheCreation.Ephemeral1h = 1_000_000
	cases := []struct {
		model string
		batch bool
		want  float64
	}{
		{"claude-sonnet-5-5", false, 2 + 10 + 0.10 + 2.50 + 4},
		{"claude-sonnet-5-5", true, (2 + 10 + 0.10 + 2.50 + 4) / 2},
		{"claude-opus-5-5-20261001", false, 4 + 20 + 0.20 + 5 + 8},
		// 4 M prompt tokens: above the 100k threshold of Haiku.
		{"claude-haiku-5-5", false, 0.50 + 2.50 + 0.05 + 0.625 + 1},
	}
	for _, c := range cases {
		got, err := ClaudeCostUSD(c.model, u, c.batch)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s batch=%v: cost %.4f, want %.4f", c.model, c.batch, got, c.want)
		}
	}
	small := ClaudeUsage{InputTokens: 1000, OutputTokens: 100}
	if got, _ := ClaudeCostUSD("claude-haiku-5-5", small, false); math.Abs(got-(1000*0.10+100*0.50)/1e6) > 1e-12 {
		t.Errorf("haiku short prompt: cost %g", got)
	}
	if _, err := ClaudeCostUSD("claude-unknown", small, true); err == nil || !strings.Contains(err.Error(), "no price") {
		t.Fatalf("unknown model must fail, got %v", err)
	}
}
