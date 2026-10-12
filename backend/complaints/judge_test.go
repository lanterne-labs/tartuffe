package complaints

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"digital-mirror/backend/llm"
)

// judgeStub answers each call with the next reply of replies (the last one
// repeats) and counts the calls.
func judgeStub(t *testing.T, replies ...string) (*OpenAIJudge, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(calls.Add(1))
		reply := replies[min(n, len(replies))-1]
		resp := map[string]any{"choices": []map[string]any{{"message": map[string]string{"role": "assistant", "content": reply}}}}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	cfg := llm.Config{BaseURL: srv.URL + "/v1", APIKey: "test", ChatModel: "stub"}
	return &OpenAIJudge{client: cfg.NewClient("complaint-judge-test")}, &calls
}

// Prose before the JSON gets one corrective retry; the JSON of the second
// reply is the verdict.
func TestOpenAIJudge_CorrectiveRetry(t *testing.T) {
	j, calls := judgeStub(t,
		"The complaint is right.\n{\"reason\": \"misquote\", \"verdict\": \"delete_target\"}",
		`{"reason": "misquote", "verdict": "delete_target"}`)
	v, err := j.Judge(context.Background(), "PROMPT")
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if v.Verdict != VerdictDeleteTarget || v.Reason != "misquote" {
		t.Fatalf("verdict = %+v, want delete_target/misquote", v)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
}

// A second prose reply is an error, never a default verdict.
func TestOpenAIJudge_TwiceInvalid(t *testing.T) {
	j, calls := judgeStub(t, "The complaint is right.")
	_, err := j.Judge(context.Background(), "PROMPT")
	if err == nil || !strings.Contains(err.Error(), "invalid JSON twice") {
		t.Fatalf("err = %v, want an error on the second invalid reply", err)
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("calls = %d, want 2", n)
	}
}
