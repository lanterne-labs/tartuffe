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
// -llm-claude=<model> sends every call through the Claude Message Batches API
// instead (ANTHROPIC_API_KEY of .env). A task that thinks runs at effort
// medium, the others at effort low. The replies are stored by request in
// -llm-claude-store, so a rerun costs nothing; -llm-claude-max-usd caps the
// spend of that store. The cases of a golden run concurrently (Group,
// Parallel; ForEach for an eval) to fill a batch.
//
// -llm-claude-team=<model> sends every call through `claude -p` on the seats
// of the Team plan instead (CLAUDE_TEAM_PLAN_TOKENS of .env; a seat at its
// usage limit gives way to the next), with the same effort rule. The cases run concurrently too; the
// endpoint limits the number of CLI processes. It cannot go with -llm-claude.
//
// -llm-claude-models=task=model[:effort],... changes the model, and the
// effort (low, medium or high) when given, of some tasks on top of
// -llm-claude or -llm-claude-team: for example
// audit=claude-opus-5-5:high,verify=claude-opus-5-5. The
// usage reports give one line per task and model.
//
// Only the test binaries of packages that import llmtest accept the flags.
package llmtest

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/llm"

	"github.com/joho/godotenv"
)

var (
	apiFlag   = flag.String("llm-api", "", "LLM goldens: OpenAI-compatible base URL that replaces OPEN_AI_API of .env")
	modelFlag = flag.String("llm-model", "", "LLM goldens: model name that replaces OPEN_AI_CHAT_MODEL of .env")
	thinkFlag = flag.String("llm-think", "", "LLM goldens: comma-separated tasks that think (llm.AllTasks), or none; replaces the production set")

	claudeFlag  = flag.String("llm-claude", "", "LLM goldens: Claude model (claude-haiku-5-5, claude-sonnet-5-5, claude-opus-5-5); every call goes through the Message Batches API")
	claudeUSD   = flag.Float64("llm-claude-max-usd", 25, "LLM goldens: spend cap of the Claude reply store, in USD")
	claudeStore = flag.String("llm-claude-store", "", "LLM goldens: directory of the Claude reply store (default: <user cache dir>/mirror/claude-batches)")
	claudeIdle  = flag.Duration("llm-claude-idle", 5*time.Second, "LLM goldens: a batch opens when no new call came for this time")
	// The batch doc recommends 1h; the pilot measures 5m too.
	claudeCacheTTL = flag.String("llm-claude-cache-ttl", "1h", "LLM goldens: TTL of the Claude cache breakpoints: 5m, 1h or off")

	claudeTeamFlag = flag.String("llm-claude-team", "", "LLM goldens: Claude model; every call goes through `claude -p` on the Team seats of CLAUDE_TEAM_PLAN_TOKENS")

	claudeModelsFlag = flag.String("llm-claude-models", "", "LLM goldens: per-task Claude models on top of -llm-claude or -llm-claude-team: task=model[:effort],task=model[:effort]")
)

var (
	claudeOnce     sync.Once
	claudeEndpoint *llm.ClaudeBatchEndpoint
	claudeErr      error

	claudeTeamOnce     sync.Once
	claudeTeamEndpoint *llm.ClaudeTeamEndpoint
	claudeTeamErr      error
)

// Claude reports whether the run sends its calls to Claude: through the
// batches (-llm-claude) or through the CLI on the Team seats (-llm-claude-team).
// Both runs need concurrent cases.
func Claude() bool { return *claudeFlag != "" || *claudeTeamFlag != "" }

// Group runs the cases of a golden. When Claude() is true, it runs them as one
// subtest, so the cases that call Parallel run concurrently and the parent
// sees their results (and keeps its context) after they all end. Without it,
// it calls cases directly: the local run keeps its order and names.
func Group(t *testing.T, cases func(t *testing.T)) {
	t.Helper()
	if !Claude() {
		cases(t)
		return
	}
	t.Run("cases", cases)
}

// Parallel marks a case of a Group as parallel when Claude() is true.
func Parallel(t *testing.T) {
	t.Helper()
	if Claude() {
		t.Parallel()
	}
}

// KnownGap reports whether a documented gap of the local model excuses a
// miss of a case in this run, and logs the gap when it does. gap is the
// reason of the gap, or "" for a case without one. A gap holds only for the
// local model: when Claude() is true, the case must pass and KnownGap
// returns false. A golden counts a gap apart from its passes and failures.
func KnownGap(t *testing.T, gap string) bool {
	t.Helper()
	if gap == "" || Claude() {
		return false
	}
	t.Logf("known gap of the local model: %s", gap)
	return true
}

// ForEach calls fn(i) for each i from 0 to n-1 and returns when all calls
// end. When Claude() is false, it calls them in order. When it is true, it
// calls them all at once, so their LLM calls fill one batch or the CLI
// processes of the Team seat. fn must not call t.Fatal or t.FailNow: when
// Claude() is true, it runs on its own goroutine.
func ForEach(n int, fn func(i int)) {
	if !Claude() {
		for i := 0; i < n; i++ {
			fn(i)
		}
		return
	}
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			fn(i)
		}()
	}
	wg.Wait()
}

// Setup loads the repo .env over the shell environment (a stale shell
// variable must not choose the model, as in config.Load), then applies
// -llm-api, -llm-model and -llm-think. It skips the test when OPEN_AI_KEY is
// not set: the goldens call a real model and never run in CI. Last, it
// installs the routing of -llm-claude or -llm-claude-team, with the
// per-task models of -llm-claude-models, until the test ends.
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
	var routing *llm.Routing
	switch {
	case *claudeFlag != "" && *claudeTeamFlag != "":
		t.Fatal("-llm-claude and -llm-claude-team cannot go together")
	case *claudeTeamFlag != "":
		e, err := newClaudeTeamEndpoint()
		if err != nil {
			t.Fatalf("-llm-claude-team: %v", err)
		}
		routing = llm.ClaudeRouting(llm.EndpointClaudeTeam, e, *claudeTeamFlag)
		t.Cleanup(func() {
			t.Logf("claude team usage (cumulative in this test binary):\n%s", e.UsageReport())
		})
	case *claudeFlag != "":
		e, err := claudeBatchEndpoint()
		if err != nil {
			t.Fatalf("-llm-claude: %v", err)
		}
		routing = llm.ClaudeRouting(llm.EndpointClaudeBatch, e, *claudeFlag)
		t.Cleanup(func() {
			t.Logf("claude usage (cumulative in this test binary):\n%s", e.UsageReport())
		})
	case *claudeModelsFlag != "":
		t.Fatal("-llm-claude-models needs -llm-claude or -llm-claude-team")
	default:
		return
	}
	routing, err := routing.ApplyOverrides(*claudeModelsFlag)
	if err != nil {
		t.Fatalf("-llm-claude-models: %v", err)
	}
	restore, err := llm.SetRouting(routing)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	t.Logf("LLM routes for this run:\n%s", routing)
}

// claudeBatchEndpoint builds the one endpoint of the test binary: the
// count of sends per request and the usage span every test.
func claudeBatchEndpoint() (*llm.ClaudeBatchEndpoint, error) {
	claudeOnce.Do(func() {
		client, err := llm.NewClaudeBatchClientFromEnv()
		if err != nil {
			claudeErr = err
			return
		}
		dir := *claudeStore
		if dir == "" {
			cache, err := os.UserCacheDir()
			if err != nil {
				claudeErr = fmt.Errorf("no -llm-claude-store and no user cache dir: %w", err)
				return
			}
			dir = filepath.Join(cache, "mirror", "claude-batches")
		}
		claudeEndpoint = &llm.ClaudeBatchEndpoint{
			Runner:   &llm.ClaudeBatchRunner{Client: client, Dir: dir, MaxUSD: *claudeUSD},
			Idle:     *claudeIdle,
			CacheTTL: *claudeCacheTTL,
		}
		claudeErr = claudeEndpoint.Validate()
	})
	return claudeEndpoint, claudeErr
}

// newClaudeTeamEndpoint builds the one Team endpoint of the test binary: the
// process limit, the seats that are down and the usage span every test.
func newClaudeTeamEndpoint() (*llm.ClaudeTeamEndpoint, error) {
	claudeTeamOnce.Do(func() {
		claudeTeamEndpoint, claudeTeamErr = llm.NewClaudeTeamEndpointFromEnv()
	})
	return claudeTeamEndpoint, claudeTeamErr
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
