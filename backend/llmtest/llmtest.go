// Package llmtest sets up the local-only LLM golden tests.
//
// A golden runs against the model of the repo .env, with the production
// thinking setting of each task (llm/task.go). Three test flags change that
// for one run, to measure a candidate model or a task with thinking:
//
//	go test ./backend/core -run Golden -timeout 6h -args \
//	    -llm-api=http://127.0.0.1:8095/v1 -llm-model=candidate \
//	    -llm-think=stance-analyze,detect
//
// -llm-think gives the exact set of tasks that think ("none" for no task).
//
// Only the test binaries of packages that import llmtest accept the flags.
package llmtest

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	"digital-mirror/backend/llm"

	"github.com/joho/godotenv"
)

var (
	apiFlag   = flag.String("llm-api", "", "LLM goldens: OpenAI-compatible base URL that replaces OPEN_AI_API of .env")
	modelFlag = flag.String("llm-model", "", "LLM goldens: model name that replaces OPEN_AI_CHAT_MODEL of .env")
	thinkFlag = flag.String("llm-think", "", "LLM goldens: comma-separated tasks that think (llm.AllTasks), or none; replaces the production set")
)

// Setup loads the repo .env over the shell environment (a stale shell
// variable must not choose the model, as in config.Load), then applies
// -llm-api, -llm-model and -llm-think. It skips the test when OPEN_AI_KEY is
// not set: the goldens call a real model and never run in CI.
func Setup(t *testing.T) {
	t.Helper()
	if *thinkFlag != "" {
		tasks, err := llm.ParseTasks(*thinkFlag)
		if err != nil {
			t.Fatalf("-llm-think: %v", err)
		}
		t.Cleanup(llm.OverrideThinking(tasks))
		t.Logf("thinking tasks for this run: %v", tasks)
	}
	if root, ok := repoRoot(); ok {
		_ = godotenv.Overload(filepath.Join(root, ".env"))
	}
	if *apiFlag != "" {
		t.Setenv("OPEN_AI_API", *apiFlag)
	}
	if *modelFlag != "" {
		t.Setenv("OPEN_AI_CHAT_MODEL", *modelFlag)
	}
	if os.Getenv("OPEN_AI_KEY") == "" {
		t.Skip("local-only LLM golden: OPEN_AI_KEY is not set")
	}
}

// Context returns a context that ends 1 min before the deadline of the go
// test run (-timeout). A slower model or a judge that thinks needs more time;
// the time limit belongs to the run, not to each test. The minute lets a
// golden that runs out of time still log its scorecard before go test stops
// the binary.
func Context(t *testing.T) (context.Context, context.CancelFunc) {
	t.Helper()
	if deadline, ok := t.Deadline(); ok {
		return context.WithDeadline(context.Background(), deadline.Add(-time.Minute))
	}
	return context.WithCancel(context.Background())
}

// repoRoot returns the nearest parent directory of the working directory
// that holds go.mod.
func repoRoot() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}
