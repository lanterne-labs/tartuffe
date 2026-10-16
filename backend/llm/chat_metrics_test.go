package llm

import (
	"context"
	"testing"

	"digital-mirror/backend/metrics"

	"github.com/prometheus/client_golang/prometheus/testutil"
)

// A routed call counts in the LLM request metrics, under the model of its
// route: the LLM dashboard shows the Claude calls of the nightly.
func TestChatRoutedCallCountsInTheMetrics(t *testing.T) {
	const model = "claude-sonnet-5-5"
	count := func(status string) float64 {
		return testutil.ToFloat64(metrics.LLMRequestsTotal.WithLabelValues("chat", model, status))
	}
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}, MaxTokens: 10, Task: TaskDetect}
	for _, c := range []struct {
		stdout, status string
		code           int
	}{
		{teamSuccess, "success", 0},
		{"", "error", 1},
	} {
		bin, _ := fakeClaude(t, c.stdout, "boom", c.code)
		setRouting(t, ClaudeRouting(EndpointClaudeTeam, teamEndpoint(bin), model))
		before := count(c.status)
		_, _, err := (&Client{}).Chat(context.Background(), req)
		if (err == nil) != (c.status == "success") {
			t.Fatalf("%s call: err = %v", c.status, err)
		}
		if got := count(c.status) - before; got != 1 {
			t.Fatalf("%s count grew by %v, want 1", c.status, got)
		}
	}
}
