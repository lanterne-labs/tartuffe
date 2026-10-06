package llm

import (
	"context"
	"encoding/json"
	"testing"
)

// The production table: the grouping gate thinks, the other tasks and a call
// without a task do not.
func TestThinksProductionTable(t *testing.T) {
	if !Thinks(TaskOccurrence) || Thinks(TaskDetect) || Thinks("") {
		t.Fatalf("production table changed: occurrence=%v detect=%v none=%v", Thinks(TaskOccurrence), Thinks(TaskDetect), Thinks(""))
	}
}

// An override replaces the whole table until restore.
func TestOverrideThinking(t *testing.T) {
	restore := OverrideThinking([]Task{TaskDetect})
	if !Thinks(TaskDetect) || Thinks(TaskOccurrence) {
		t.Fatalf("override not applied")
	}
	restore()
	if Thinks(TaskDetect) || !Thinks(TaskOccurrence) {
		t.Fatalf("restore did not bring the production table back")
	}
}

func TestParseTasks(t *testing.T) {
	got, err := ParseTasks("detect, verify")
	if err != nil || len(got) != 2 || got[0] != TaskDetect || got[1] != TaskVerify {
		t.Fatalf("ParseTasks = %v, %v", got, err)
	}
	if got, err := ParseTasks("none"); err != nil || len(got) != 0 {
		t.Fatalf(`ParseTasks("none") = %v, %v`, got, err)
	}
	if _, err := ParseTasks("detect,typo"); err == nil {
		t.Fatal("an unknown task must be an error")
	}
}

// Chat sends the thinking switch and the thinking budget for a task that
// thinks, and neither for a task that does not.
func TestChatAppliesTaskThinking(t *testing.T) {
	const ok = `{"choices":[{"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`
	send := func(task Task) map[string]any {
		c, got := chatTestClient(t, 200, ok)
		if _, _, err := c.Chat(context.Background(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}, MaxTokens: 256, Task: task}); err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		if err := json.Unmarshal(*got, &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	body := send(TaskOccurrence)
	kw, _ := body["chat_template_kwargs"].(map[string]any)
	if kw["enable_thinking"] != true || body["max_tokens"] != float64(256+ThinkingBudgetTokens) || body["thinking_budget_tokens"] != float64(ThinkingBudgetTokens) {
		t.Fatalf("thinking task: body %v", body)
	}
	if _, sent := body["Task"]; sent {
		t.Fatal("Task must not be sent to the server")
	}
	body = send(TaskDetect)
	if _, has := body["chat_template_kwargs"]; has || body["thinking_budget_tokens"] != nil || body["max_tokens"] != float64(256) {
		t.Fatalf("plain task: body %v", body)
	}
}
