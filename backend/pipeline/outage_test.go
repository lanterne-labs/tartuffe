package pipeline

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins"
)

var errDown = fmt.Errorf("%w: primary down; fallback failed", llm.ErrUnavailable)

// fakePlugin ingests and backfills by returning err, recording each call.
type fakePlugin struct {
	name  string
	err   error
	calls *[]string
}

func (f fakePlugin) Name() string { return f.name }

func (f fakePlugin) Ingest(context.Context, *deps.Deps, []plugins.Target, plugins.IngestOptions) (plugins.IngestStats, error) {
	*f.calls = append(*f.calls, "ingest:"+f.name)
	return plugins.IngestStats{}, f.err
}

func (f fakePlugin) Backfill(context.Context, *deps.Deps, time.Time) error {
	*f.calls = append(*f.calls, "backfill:"+f.name)
	return f.err
}

func withPlugins(t *testing.T, ps ...plugins.Plugin) {
	t.Helper()
	saved := Plugins
	Plugins = ps
	t.Cleanup(func() { Plugins = saved })
}

// TestLLMGate: the first phase that reports the LLM unavailable closes the
// gate for every later LLM phase and names the run's error; other errors
// leave it open.
func TestLLMGate(t *testing.T) {
	var g llmGate
	g.observe("ingest", errors.New("serp quota"))
	if !g.open("stance-fanout") || g.err() != nil {
		t.Fatal("a non-LLM failure must not stop the run")
	}
	g.observe("stance-fanout", fmt.Errorf("routing fact 7: %w", errDown))
	g.observe("stance-merge", errDown)
	if g.open("contradictions") || g.open("translate") {
		t.Fatal("no LLM phase may start after the outage")
	}
	err := g.err()
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("run error %v must wrap ErrUnavailable", err)
	}
	if want := "LLM unavailable: stopped after phase stance-fanout"; err.Error() != want {
		t.Errorf("run error = %q, want %q", err, want)
	}
}

// TestIngestStopsAfterUnavailable: once a plugin reports the LLM
// unavailable, the later plugins are not started (they would walk their
// items with fast failures and burn scrape credits); a plugin failing for
// another reason does not stop the phase.
func TestIngestStopsAfterUnavailable(t *testing.T) {
	var calls []string
	withPlugins(t,
		fakePlugin{name: "a", err: errors.New("serp quota"), calls: &calls},
		fakePlugin{name: "b", err: errDown, calls: &calls},
		fakePlugin{name: "c", calls: &calls},
	)
	_, errs, err := Ingest(context.Background(), nil, nil, plugins.IngestOptions{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(calls) != "[ingest:a ingest:b]" {
		t.Errorf("calls = %v, want a and b only", calls)
	}
	if !errors.Is(errs["b"], llm.ErrUnavailable) || !llmDown(errs) {
		t.Errorf("errs = %v, want b's outage reported", errs)
	}
}

// TestBackfillStopsAfterUnavailable: the same rule holds for backfills, and
// a restricted run (the LLM-free backfills after an outage) runs only those.
func TestBackfillStopsAfterUnavailable(t *testing.T) {
	var calls []string
	withPlugins(t,
		fakePlugin{name: "a", err: errDown, calls: &calls},
		fakePlugin{name: "b", calls: &calls},
	)
	deadline := time.Now().Add(time.Hour)
	errs := Backfill(context.Background(), nil, deadline, nil)
	if fmt.Sprint(calls) != "[backfill:a]" || !llmDown(errs) {
		t.Errorf("calls = %v, errs = %v: b must not start after a's outage", calls, errs)
	}

	calls = nil
	if errs := Backfill(context.Background(), nil, deadline, []string{"b"}); len(errs) != 0 {
		t.Fatalf("errs = %v", errs)
	}
	if fmt.Sprint(calls) != "[backfill:b]" {
		t.Errorf("calls = %v, want b only", calls)
	}
}
