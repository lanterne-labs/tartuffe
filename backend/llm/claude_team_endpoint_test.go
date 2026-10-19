package llm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeClaude writes a fake CLI into a temp dir. It records its args (NUL
// separated), stdin and env, prints stdout to stdout and stderr to stderr,
// and exits with code.
func fakeClaude(t *testing.T, stdout, stderr string, code int) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	for name, content := range map[string]string{"stdout": stdout, "stderr": stderr} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := fmt.Sprintf(`#!/bin/sh
d=%q
for a in "$@"; do printf '%%s\0' "$a"; done > "$d/args"
cat > "$d/stdin"
env > "$d/env"
cat "$d/stdout"
cat "$d/stderr" >&2
exit %d
`, dir, code)
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func teamEndpoint(bin string) *ClaudeTeamEndpoint {
	return &ClaudeTeamEndpoint{Binary: bin, seats: []claudeSeat{{name: "alice", token: "tok-alice"}}}
}

// teamOpts are the Claude settings of the calls of the team tests.
func teamOpts(task Task) ClaudeOptions {
	effort := EffortLow
	if task == TaskDetect {
		effort = EffortMedium
	}
	return ClaudeOptions{Model: "claude-haiku-5-5", Effort: effort, ThinkingTokens: 1000}
}

// teamCall calls e with the settings of teamOpts.
func teamCall(e *ClaudeTeamEndpoint, ctx context.Context, req ChatRequest) (string, *ChatResponse, error) {
	return e.Call(ctx, req, teamOpts(req.Task))
}

const teamSuccess = `{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn",` +
	`"result":"` + "```json\\n{\\\"ok\\\":true}\\n```" + `","api_error_status":null,"total_cost_usd":0.0008,` +
	`"usage":{"input_tokens":2,"cache_creation_input_tokens":560,"cache_read_input_tokens":10,"output_tokens":9,` +
	`"output_tokens_details":{"thinking_tokens":3},"cache_creation":{"ephemeral_1h_input_tokens":560,"ephemeral_5m_input_tokens":0}},"duration_ms":899}`

func TestClaudeTeamEndpoint_Success(t *testing.T) {
	bin, dir := fakeClaude(t, teamSuccess, "", 0)
	t.Setenv("ANTHROPIC_API_KEY", "sk-api")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "auth")
	t.Setenv("ANTHROPIC_BASE_URL", "https://proxy.example")
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", "inherited")
	t.Setenv("CLAUDE_CODE_USE_BEDROCK", "1")
	t.Setenv("CLAUDE_CODE_ENTRYPOINT", "cli")
	t.Setenv("CLAUDE_CODE_SSE_PORT", "1234")
	t.Setenv("CLAUDECODE", "1")
	t.Setenv("CLAUDE_TEAM_PLAN_TOKENS", "alice=tok-alice,bob=tok-bob")
	t.Setenv("KEEP_ME", "yes")
	t.Setenv("DISABLE_AUTOUPDATER", "0")
	e := teamEndpoint(bin)
	text, resp, err := teamCall(e, context.Background(), ChatRequest{
		Task:           TaskDetect,
		MaxTokens:      200,
		ResponseFormat: &ResponseFormat{Type: "json_object"},
		Messages: []ChatMessage{
			{Role: "system", Content: "You are a judge."},
			{Role: "user", Content: "Is it a contradiction?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if text != `{"ok":true}` {
		t.Fatalf("text %q: the code fence must go", text)
	}
	if resp.Usage.PromptTokens != 572 || resp.Usage.CompletionTokens != 9 || resp.Usage.TotalTokens != 581 {
		t.Fatalf("usage %+v", resp.Usage)
	}

	args := strings.Split(strings.TrimSuffix(readFile(t, filepath.Join(dir, "args")), "\x00"), "\x00")
	want := []string{"-p", "--output-format", "json", "--model", "claude-haiku-5-5", "--effort", "medium",
		"--system-prompt", "You are a judge.", "--tools", "", "--no-session-persistence", "--strict-mcp-config",
		"--setting-sources", "", "--disable-slash-commands", "--exclude-dynamic-system-prompt-sections"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("args\n got %q\nwant %q", args, want)
	}
	if got := readFile(t, filepath.Join(dir, "stdin")); got != "Is it a contradiction?" {
		t.Fatalf("stdin %q", got)
	}
	env := map[string]string{}
	for _, line := range strings.Split(readFile(t, filepath.Join(dir, "env")), "\n") {
		k, v, _ := strings.Cut(line, "=")
		env[k] = v
	}
	for name := range env {
		if name == "CLAUDE_CODE_OAUTH_TOKEN" || name == "CLAUDE_CODE_MAX_OUTPUT_TOKENS" || name == "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC" {
			continue
		}
		for _, prefix := range []string{"ANTHROPIC_", "CLAUDE_CODE_", "CLAUDECODE", "CLAUDE_TEAM_PLAN_"} {
			if strings.HasPrefix(name, prefix) {
				t.Errorf("the child env must not hold %s", name)
			}
		}
	}
	if env["KEEP_ME"] != "yes" {
		t.Error("the child env must keep the other parent variables")
	}
	if env["CLAUDE_CODE_OAUTH_TOKEN"] != "tok-alice" {
		t.Errorf("CLAUDE_CODE_OAUTH_TOKEN = %q, want the seat token", env["CLAUDE_CODE_OAUTH_TOKEN"])
	}
	if env["DISABLE_AUTOUPDATER"] != "1" || env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"] != "1" {
		t.Errorf("DISABLE_AUTOUPDATER = %q, CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC = %q; want 1 and 1",
			env["DISABLE_AUTOUPDATER"], env["CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC"])
	}
	if env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"] != "1200" {
		t.Errorf("CLAUDE_CODE_MAX_OUTPUT_TOKENS = %q, want MaxTokens + ThinkingTokens", env["CLAUDE_CODE_MAX_OUTPUT_TOKENS"])
	}

	report := e.UsageReport()
	for _, s := range []string{"detect", "calls=1 failed=0 input=2 cache_write=560 cache_read=10 output=9 thinking=3 cost=$0.0008", "total cost=$0.0008"} {
		if !strings.Contains(report, s) {
			t.Fatalf("report has no %q:\n%s", s, report)
		}
	}
}

// The fact extractor sends its prompt as one system message: it goes on
// stdin, and the system prompt is the neutral one, not the CLI default.
func TestClaudeTeamEndpoint_LoneSystemMessage(t *testing.T) {
	bin, dir := fakeClaude(t, teamSuccess, "", 0)
	_, _, err := teamCall(teamEndpoint(bin), context.Background(), ChatRequest{
		MaxTokens: 10,
		Messages:  []ChatMessage{{Role: "system", Content: "rules and article"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(dir, "stdin")); got != "rules and article" {
		t.Fatalf("stdin %q", got)
	}
	if !strings.Contains(readFile(t, filepath.Join(dir, "args")), "--system-prompt\x00"+claudeTeamSystemPrompt+"\x00") {
		t.Fatal("no neutral system prompt")
	}
}

// The corrective retry of VerdictJudge sends the bad reply as an assistant
// turn. The CLI gets the conversation as one labelled prompt.
func TestClaudeTeamEndpoint_Conversation(t *testing.T) {
	bin, dir := fakeClaude(t, teamSuccess, "", 0)
	_, _, err := teamCall(teamEndpoint(bin), context.Background(), ChatRequest{
		MaxTokens: 10,
		Messages: []ChatMessage{
			{Role: "user", Content: "judge this"},
			{Role: "assistant", Content: "bad reply"},
			{Role: "user", Content: "reply again"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "This is a conversation. Each turn starts with a label line. Reply to the last USER turn.\n" +
		"\n=== USER ===\njudge this\n" +
		"\n=== ASSISTANT (your previous reply) ===\nbad reply\n" +
		"\n=== USER ===\nreply again\n"
	if got := readFile(t, filepath.Join(dir, "stdin")); got != want {
		t.Fatalf("stdin\n got %q\nwant %q", got, want)
	}
}

// teamSent returns the system prompt and the stdin of the last run of the
// fake CLI in dir.
func teamSent(t *testing.T, dir string) (system, stdin string) {
	t.Helper()
	args := strings.Split(readFile(t, filepath.Join(dir, "args")), "\x00")
	for i, a := range args[:len(args)-1] {
		if a == "--system-prompt" {
			system = args[i+1]
		}
	}
	return system, readFile(t, filepath.Join(dir, "stdin"))
}

// teamPrefixCall sends one user message with the system text "You are a
// judge." and returns the system prompt and the stdin of the CLI.
func teamPrefixCall(t *testing.T, e *ClaudeTeamEndpoint, dir string, task Task, prompt string) (system, stdin string) {
	t.Helper()
	_, _, err := teamCall(e, context.Background(), ChatRequest{
		Task:      task,
		MaxTokens: 10,
		Messages: []ChatMessage{
			{Role: "system", Content: "You are a judge."},
			{Role: "user", Content: prompt},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return teamSent(t, dir)
}

// The CLI caches up to the end of the user message. The second call of a
// family sends the static rules that it shares with the first call in the
// system prompt, and only the rest on stdin.
func TestClaudeTeamEndpoint_CachePrefix(t *testing.T) {
	bin, dir := fakeClaude(t, teamSuccess, "", 0)
	e := teamEndpoint(bin)
	rules := strings.Repeat("rule line\n", 250) // 2500 chars
	for _, step := range []struct {
		prompt, system, stdin string
	}{
		// The first call has no earlier prompt to learn from.
		{rules + "case A\n", "You are a judge.", rules + "case A\n"},
		{rules + "case B\n", "You are a judge.\n\n" + rules, "case B\n"},
		{rules + "case C\n", "You are a judge.\n\n" + rules, "case C\n"},
	} {
		system, stdin := teamPrefixCall(t, e, dir, TaskDetect, step.prompt)
		if system != step.system || stdin != step.stdin {
			t.Fatalf("prompt %q:\nsystem %q\nstdin %q", step.prompt[len(rules):], system, stdin)
		}
	}
}

// Two equal prompts (the votes of a judge) share the whole prompt. The CLI
// needs a user message, so the prompt goes whole on stdin.
func TestClaudeTeamEndpoint_CachePrefixEqualPrompts(t *testing.T) {
	bin, dir := fakeClaude(t, teamSuccess, "", 0)
	e := teamEndpoint(bin)
	rules := strings.Repeat("rule line\n", 250)
	for range 2 {
		system, stdin := teamPrefixCall(t, e, dir, TaskDetect, rules+"case A\n")
		if system != "You are a judge." || stdin != rules+"case A\n" {
			t.Fatalf("system %q, stdin %q: an equal prompt must go whole on stdin", system, stdin)
		}
	}
	system, stdin := teamPrefixCall(t, e, dir, TaskDetect, rules+"case B\n")
	if system != "You are a judge.\n\n"+rules || stdin != "case B\n" {
		t.Fatalf("system %q, stdin %q: a new case must split at the rules", system, stdin)
	}
}

// A shared start of less than minCachePrefixChars is not cached, so it is
// never split.
func TestClaudeTeamEndpoint_CachePrefixTooShort(t *testing.T) {
	bin, dir := fakeClaude(t, teamSuccess, "", 0)
	e := teamEndpoint(bin)
	rules := strings.Repeat("rule line\n", 150) // 1500 chars, one family
	for _, c := range []string{"case A\n", "case B\n", "case C\n"} {
		system, stdin := teamPrefixCall(t, e, dir, TaskDetect, rules+c)
		if system != "You are a judge." || stdin != rules+c {
			t.Fatalf("%q: system %q, stdin %q: a short prefix must not split", c, system, stdin)
		}
	}
}

// Two tasks are two families: one task does not learn from the prompt of the
// other.
func TestClaudeTeamEndpoint_CachePrefixPerFamily(t *testing.T) {
	bin, dir := fakeClaude(t, teamSuccess, "", 0)
	e := teamEndpoint(bin)
	rules := strings.Repeat("rule line\n", 250)
	teamPrefixCall(t, e, dir, TaskDetect, rules+"case A\n")
	system, stdin := teamPrefixCall(t, e, dir, TaskAudit, rules+"case B\n")
	if system != "You are a judge." || stdin != rules+"case B\n" {
		t.Fatalf("system %q, stdin %q: another family must not share the prefix", system, stdin)
	}
	system, stdin = teamPrefixCall(t, e, dir, TaskDetect, rules+"case C\n")
	if system != "You are a judge.\n\n"+rules || stdin != "case C\n" {
		t.Fatalf("system %q, stdin %q: the family must learn from its own last prompt", system, stdin)
	}
}

func TestClaudeTeamEndpoint_Errors(t *testing.T) {
	req := ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "q"}}}
	cases := []struct {
		name        string
		stdout      string
		stderr      string
		code        int
		unavailable bool
		retryDelay  bool // no reset text: RetryAt is now + claudeRetryDelay
		noRetryAt   bool // a reset text that does not parse: zero RetryAt
		contains    []string
		once        string // a text that the error holds 1 time
	}{
		{
			name:        "429",
			stdout:      `{"type":"result","subtype":"success","is_error":true,"result":"API Error: 429","api_error_status":429,"usage":{}}`,
			code:        1,
			unavailable: true,
			retryDelay:  true,
			contains:    []string{`seat "alice"`, "every claude seat (1) is at its usage limit"},
		},
		{
			name:        "limit text",
			stdout:      `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit · resets 3pm (Europe/Paris)","usage":{}}`,
			code:        1,
			unavailable: true,
			contains:    []string{`seat "alice"`, "it resets 3pm (Europe/Paris)"},
			once:        "3pm",
		},
		{
			name:        "limit text, reset at",
			stdout:      `{"type":"result","subtype":"success","is_error":true,"result":"Usage limit reached. Your limit resets at 3.30pm.","usage":{}}`,
			code:        1,
			unavailable: true,
			contains:    []string{"it resets 3.30pm"},
			once:        "3.30pm",
		},
		{
			name:        "limit text, Unix time",
			stdout:      `{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached|1760000000","usage":{}}`,
			code:        1,
			unavailable: true,
			contains:    []string{"it resets at 2025-10-09T08:53:20Z"},
		},
		{
			name:        "limit text, reset that does not parse",
			stdout:      `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit · resets Monday","usage":{}}`,
			code:        1,
			unavailable: true,
			noRetryAt:   true,
			contains:    []string{"it resets Monday", "the first resets at a time that is not known"},
		},
		{
			name:     "context limit",
			stdout:   `{"type":"result","subtype":"success","is_error":true,"result":"Context limit reached","usage":{}}`,
			code:     1,
			contains: []string{"Context limit reached"},
		},
		{
			name:     "error subtype",
			stdout:   `{"type":"result","subtype":"error_max_turns","is_error":false,"result":"","usage":{}}`,
			contains: []string{"error_max_turns error"},
		},
		{
			name:     "exit 0 without json",
			stdout:   "hello",
			contains: []string{"stdout is not a result object", "stdout: hello"},
		},
		{
			name:     "exit 1 with a success result",
			stdout:   teamSuccess,
			stderr:   "boom",
			code:     1,
			contains: []string{"exit status 1 with a success result", "stderr: boom"},
		},
		{
			name:     "other error",
			stdout:   `{"type":"result","subtype":"success","is_error":true,"result":"API Error: 500 overloaded","api_error_status":500,"usage":{}}`,
			code:     1,
			contains: []string{"API Error: 500 overloaded"},
		},
		{
			name:     "max_tokens",
			stdout:   `{"type":"result","subtype":"success","is_error":false,"stop_reason":"max_tokens","result":"{\"a\":","usage":{"output_tokens":10}}`,
			contains: []string{"max_tokens"},
		},
		{
			name:     "empty result",
			stdout:   `{"type":"result","subtype":"success","is_error":false,"stop_reason":"end_turn","result":"  ","usage":{}}`,
			contains: []string{"no text"},
		},
		{
			name:     "exit without json",
			stderr:   "Error: not logged in",
			code:     2,
			contains: []string{"exit status 2", "stderr: Error: not logged in", "stdout: "},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, _ := fakeClaude(t, c.stdout, c.stderr, c.code)
			e := teamEndpoint(bin)
			start := time.Now()
			_, _, err := teamCall(e, context.Background(), req)
			if err == nil {
				t.Fatal("want an error")
			}
			if got := errors.Is(err, ErrUnavailable); got != c.unavailable {
				t.Fatalf("errors.Is(err, ErrUnavailable) = %v: %v", got, err)
			}
			var ue *UnavailableError
			if got := errors.As(err, &ue); got != c.unavailable {
				t.Fatalf("errors.As(err, *UnavailableError) = %v: %v", got, err)
			}
			if c.unavailable && (ue.Endpoint != EndpointClaudeTeam || ue.RetryAt.IsZero() != c.noRetryAt) {
				t.Fatalf("a seat limit must name the team endpoint, and a retry time unless its reset does not parse: %+v", ue)
			}
			if c.noRetryAt {
				if down := e.seats[0].down; down.Before(start.Add(unknownResetSkip)) || down.After(time.Now().Add(unknownResetSkip)) {
					t.Fatalf("an unparsed reset must put the seat down for unknownResetSkip, got %v", down)
				}
			}
			if c.retryDelay && (ue.RetryAt.Before(start.Add(claudeRetryDelay)) || ue.RetryAt.After(time.Now().Add(claudeRetryDelay))) {
				t.Fatalf("an unknown reset must retry after claudeRetryDelay, got %v", ue.RetryAt)
			}
			for _, s := range c.contains {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q has no %q", err, s)
				}
			}
			if c.once != "" && strings.Count(err.Error(), c.once) != 1 {
				t.Errorf("error %q must hold %q 1 time", err, c.once)
			}
			if strings.Contains(err.Error(), "tok-alice") {
				t.Fatalf("the error shows the token: %v", err)
			}
			if !strings.Contains(e.UsageReport(), "calls=1 failed=1") {
				t.Fatalf("report:\n%s", e.UsageReport())
			}
		})
	}
}

// The reset time of a seat limit gives the retry time of the wait, 1 min
// after the reset: the next time of the clock (and of the date) after now, in
// the zone of the text, else in the local zone. A reset in the last hour gives
// now + claudeRetryDelay. A text that does not parse gives no time.
func TestClaudeReset(t *testing.T) {
	paris, err := time.LoadLocation("Europe/Paris")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 14, 14, 0, 0, 0, paris)
	cases := []struct {
		text, reset string
		at          time.Time
	}{
		{"You've hit your limit · resets 3pm (Europe/Paris)", "3pm (Europe/Paris)", time.Date(2026, 10, 14, 15, 1, 0, 0, paris)},
		{"You've hit your limit · resets 1:15am (Europe/Paris)", "1:15am (Europe/Paris)", time.Date(2026, 10, 15, 1, 16, 0, 0, paris)},
		{"You've hit your limit · resets 12pm (Europe/Paris)", "12pm (Europe/Paris)", time.Date(2026, 10, 15, 12, 1, 0, 0, paris)},
		{"You've hit your limit · resets 1.30pm (Europe/Paris)", "1.30pm (Europe/Paris)", now.Add(claudeRetryDelay)},
		{"You've hit your limit · resets 2pm (Europe/Paris)", "2pm (Europe/Paris)", now.Add(claudeRetryDelay)},
		{"You've hit your limit · resets Oct 16, 9am (Europe/Paris)", "Oct 16, 9am (Europe/Paris)", time.Date(2026, 10, 16, 9, 1, 0, 0, paris)},
		{"You've hit your limit · resets Oct 14, 9am (Europe/Paris)", "Oct 14, 9am (Europe/Paris)", time.Date(2027, 10, 14, 9, 1, 0, 0, paris)},
		{"You've hit your limit · resets Oct 14, 1:30pm (Europe/Paris)", "Oct 14, 1:30pm (Europe/Paris)", now.Add(claudeRetryDelay)},
		{"Claude AI usage limit reached|1760000000", "at 2025-10-09T08:53:20Z", time.Unix(1760000000+60, 0)},
		{"You've hit your limit · resets Feb 30, 9am (Europe/Paris)", "Feb 30, 9am (Europe/Paris)", time.Time{}},
		{"You've hit your limit · resets Foo 3, 9am (Europe/Paris)", "Foo 3, 9am (Europe/Paris)", time.Time{}},
		{"You've hit your limit · resets 3pm (Mars/Olympus)", "3pm (Mars/Olympus)", time.Time{}},
		{"You've hit your limit · resets Monday", "Monday", time.Time{}},
		{"API Error: 429", "", time.Time{}},
	}
	for _, c := range cases {
		reset, at := claudeReset(c.text, now)
		if reset != c.reset || !at.Equal(c.at) {
			t.Errorf("claudeReset(%q) = (%q, %v), want (%q, %v)", c.text, reset, at, c.reset, c.at)
		}
	}
	local := time.Date(2026, 10, 14, 14, 0, 0, 0, time.Local)
	reset, at := claudeReset("Usage limit reached. Your limit resets at 3.30pm.", local)
	if want := time.Date(2026, 10, 14, 15, 31, 0, 0, time.Local); reset != "3.30pm" || !at.Equal(want) {
		t.Errorf("a clock without a zone: (%q, %v), want (\"3.30pm\", %v)", reset, at, want)
	}
}

func TestClaudeTeamEndpoint_StderrSnippet(t *testing.T) {
	bin, _ := fakeClaude(t, "", strings.Repeat("z", 2000), 1)
	_, _, err := teamCall(teamEndpoint(bin), context.Background(), ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "q"}}})
	if err == nil {
		t.Fatal("want an error")
	}
	if n := strings.Count(err.Error(), "z"); n != maxOutputSnippet {
		t.Fatalf("stderr snippet of %d bytes, want %d", n, maxOutputSnippet)
	}

	long := `{"type":"result","subtype":"success","is_error":true,"result":"` + strings.Repeat("z", 2000) + `","usage":{}}`
	bin, _ = fakeClaude(t, long, "", 1)
	_, _, err = teamCall(teamEndpoint(bin), context.Background(), ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "q"}}})
	if err == nil {
		t.Fatal("want an error")
	}
	if n := strings.Count(err.Error(), "z"); n != maxOutputSnippet {
		t.Fatalf("result snippet of %d bytes, want %d", n, maxOutputSnippet)
	}
}

// A bad setup or request fails before the CLI starts.
func TestClaudeTeamEndpoint_RefusesBeforeTheCLI(t *testing.T) {
	bin, dir := fakeClaude(t, teamSuccess, "", 0)
	user := []ChatMessage{{Role: "user", Content: "q"}}
	opts := teamOpts("")
	cases := []struct {
		name string
		edit func(e *ClaudeTeamEndpoint)
		opts ClaudeOptions
		msgs []ChatMessage
		want string
	}{
		{"struct literal", func(e *ClaudeTeamEndpoint) { e.seats = nil }, opts, user, "no seat"},
		{"no effort", func(*ClaudeTeamEndpoint) {}, ClaudeOptions{Model: "m"}, user, "has no effort"},
		{"last turn assistant", func(*ClaudeTeamEndpoint) {}, opts, []ChatMessage{{Role: "user", Content: "q"}, {Role: "assistant", Content: "a"}}, `last turn is "assistant"`},
	}
	for _, c := range cases {
		e := teamEndpoint(bin)
		c.edit(e)
		_, _, err := e.Call(context.Background(), ChatRequest{MaxTokens: 10, Messages: c.msgs}, c.opts)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error %v, want %q", c.name, err, c.want)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "args")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the CLI must not start: %v", err)
	}
}

// A cancelled call returns the context error and frees its process slot.
func TestClaudeTeamEndpoint_Cancel(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stdout"), []byte(teamSuccess), 0o644); err != nil {
		t.Fatal(err)
	}
	// The prompt "slow" makes the fake CLI wait; exec keeps one process.
	script := fmt.Sprintf(`#!/bin/sh
d=%q
if [ "$(cat)" = slow ]; then touch "$d/started"; exec sleep 30; fi
cat "$d/stdout"
`, dir)
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := teamEndpoint(bin)
	e.MaxProcs = 1
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, _, err := teamCall(e, ctx, ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "slow"}}})
		done <- err
	}()
	for {
		if _, err := os.Stat(filepath.Join(dir, "started")); err == nil {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("the call ended before the cancel: %v", err)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), `seat "alice"`) {
		t.Fatalf("want the context error with the seat, got %v", err)
	}
	if _, _, err := teamCall(e, context.Background(), ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "q"}}}); err != nil {
		t.Fatalf("the slot of the cancelled call must be free: %v", err)
	}
}

// A CLI that does not reply within CallTimeout is killed: the call fails
// with the seat named, and the slot is free again.
func TestClaudeTeamEndpoint_CallTimeout(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stdout"), []byte(teamSuccess), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
d=%q
if [ "$(cat)" = slow ]; then exec sleep 30; fi
cat "$d/stdout"
`, dir)
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := teamEndpoint(bin)
	e.MaxProcs = 1
	e.CallTimeout = 200 * time.Millisecond
	start := time.Now()
	_, _, err := teamCall(e, context.Background(), ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "slow"}}})
	if err == nil || !strings.Contains(err.Error(), `seat "alice": no reply within`) {
		t.Fatalf("want the timeout error with the seat, got %v", err)
	}
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("the call took %s, want it cut at the timeout", took)
	}
	if _, _, err := teamCall(e, context.Background(), ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "q"}}}); err != nil {
		t.Fatalf("the slot of the timed-out call must be free: %v", err)
	}
}

// At most MaxProcs processes run at one time. Each fake CLI counts the
// running processes when it starts; the test fails only on a count above
// MaxProcs, so a slow machine cannot fail it.
func TestClaudeTeamEndpoint_MaxProcs(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "stdout"), []byte(teamSuccess), 0o644); err != nil {
		t.Fatal(err)
	}
	script := fmt.Sprintf(`#!/bin/sh
d=%q
cat > /dev/null
touch "$d/run.$$"
ls "$d" | grep -c '^run\.' >> "$d/counts"
sleep 0.3
rm "$d/run.$$"
cat "$d/stdout"
`, dir)
	bin := filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e := teamEndpoint(bin)
	e.MaxProcs = 2
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := teamCall(e, context.Background(), ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "q"}}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	peak := 0
	for _, line := range strings.Fields(readFile(t, filepath.Join(dir, "counts"))) {
		n, err := strconv.Atoi(line)
		if err != nil {
			t.Fatal(err)
		}
		peak = max(peak, n)
	}
	if peak == 0 || peak > e.MaxProcs {
		t.Fatalf("peak of %d processes, want 1 to %d", peak, e.MaxProcs)
	}
}

// The seats keep the order of the list; a first seat moves to the front and
// the seats before it follow the others.
func TestClaudeSeats(t *testing.T) {
	for _, c := range []struct {
		first string
		want  []claudeSeat
	}{
		{"", []claudeSeat{{name: "alice", token: "tok-a"}, {name: "bob", token: "tok=b"}, {name: "carol", token: "tok-c"}}},
		{"alice", []claudeSeat{{name: "alice", token: "tok-a"}, {name: "bob", token: "tok=b"}, {name: "carol", token: "tok-c"}}},
		{"bob", []claudeSeat{{name: "bob", token: "tok=b"}, {name: "carol", token: "tok-c"}, {name: "alice", token: "tok-a"}}},
		{"carol", []claudeSeat{{name: "carol", token: "tok-c"}, {name: "alice", token: "tok-a"}, {name: "bob", token: "tok=b"}}},
	} {
		got, err := claudeSeats(" alice = tok-a ,bob=tok=b, carol=tok-c", c.first)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("first %q: got %v, %v; want %v", c.first, got, err, c.want)
		}
	}
	for _, c := range []struct{ tokens, seat, want string }{
		{"alice=tok-a", "bob", `seat "bob" is not in`},
		{"alice=tok-a,tok-b", "", "entry 2 is not name=token"},
		{"alice=tok-a,=tok-b", "alice", "entry 2 is not name=token"},
		{"alice=", "", "entry 1 is not name=token"},
		{"alice=tok-a,alice=tok-b", "alice", `seat "alice" is in the list 2 times`},
	} {
		_, err := claudeSeats(c.tokens, c.seat)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%q / %q: error %v, want %q", c.tokens, c.seat, err, c.want)
			continue
		}
		if strings.Contains(err.Error(), "tok-") {
			t.Errorf("%q: the error shows a token: %v", c.tokens, err)
		}
	}
}

func TestNewClaudeTeamEndpointFromEnv(t *testing.T) {
	bin, dir := fakeClaude(t, teamSuccess, "", 0)
	t.Setenv("PATH", dir)
	t.Setenv("CLAUDE_TEAM_PLAN_TOKENS", "alice=tok-a,bob=tok-b")
	t.Setenv("CLAUDE_TEAM_PLAN_SEAT", "bob")
	e, err := NewClaudeTeamEndpointFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"bob", "alice"}; !reflect.DeepEqual(e.SeatNames(), want) || e.seats[0].token != "tok-b" || e.Binary != bin {
		t.Fatalf("seats %q, binary %q", e.SeatNames(), e.Binary)
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := NewClaudeTeamEndpointFromEnv(); err == nil {
		t.Fatal("no claude CLI on the PATH must fail")
	}
	t.Setenv("PATH", dir)
	t.Setenv("CLAUDE_TEAM_PLAN_SEAT", "carol")
	if _, err := NewClaudeTeamEndpointFromEnv(); err == nil {
		t.Fatal("an unknown seat must fail")
	}
	t.Setenv("CLAUDE_TEAM_PLAN_SEAT", "")
	e, err = NewClaudeTeamEndpointFromEnv()
	if want := []string{"alice", "bob"}; err != nil || !reflect.DeepEqual(e.SeatNames(), want) {
		t.Fatalf("no first seat: seats %q, %v; want the order of the list", e.SeatNames(), err)
	}
	t.Setenv("CLAUDE_TEAM_PLAN_TOKENS", "")
	if _, err := NewClaudeTeamEndpointFromEnv(); err == nil {
		t.Fatal("no tokens must fail")
	}
}

const teamLimit = `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit · resets 3pm (Europe/Paris)","usage":{}}`

// rotatingClaude writes a fake CLI that answers with the result limit on the
// seats whose token is in a "limited" file of dir (one token per line), and
// with teamSuccess on the others. It appends the token of each call to
// "tokens" in dir.
func rotatingClaude(t *testing.T, limit string) (bin, dir string) {
	t.Helper()
	dir = t.TempDir()
	for name, content := range map[string]string{"stdout": teamSuccess, "limit": limit, "limited": ""} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := fmt.Sprintf(`#!/bin/sh
d=%q
cat > /dev/null
echo "$CLAUDE_CODE_OAUTH_TOKEN" >> "$d/tokens"
if grep -qx "$CLAUDE_CODE_OAUTH_TOKEN" "$d/limited"; then cat "$d/limit"; exit 1; fi
cat "$d/stdout"
`, dir)
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, dir
}

// A seat at its usage limit gives way to the next seat in the same call, and
// stays down until its reset. When every seat is down, the call fails with
// ErrUnavailable and the earliest reset; a seat whose reset passed serves
// again.
func TestClaudeTeamEndpoint_Rotation(t *testing.T) {
	bin, dir := rotatingClaude(t, teamLimit)
	e := &ClaudeTeamEndpoint{Binary: bin, seats: []claudeSeat{
		{name: "alice", token: "tok-alice"}, {name: "bob", token: "tok-bob"}, {name: "carol", token: "tok-carol"},
	}}
	req := ChatRequest{MaxTokens: 10, ResponseFormat: &ResponseFormat{Type: "json_object"}, Messages: []ChatMessage{{Role: "user", Content: "q"}}}
	tokens := func() []string {
		b, err := os.ReadFile(filepath.Join(dir, "tokens"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.Split(strings.TrimSpace(string(b)), "\n")
	}
	limit := func(tokens ...string) {
		if err := os.WriteFile(filepath.Join(dir, "limited"), []byte(strings.Join(tokens, "\n")+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	limit("tok-alice")
	if text, _, err := teamCall(e, context.Background(), req); err != nil || text != `{"ok":true}` {
		t.Fatalf("call 1: %q, %v; want the reply of bob", text, err)
	}
	if want := []string{"tok-alice", "tok-bob"}; !reflect.DeepEqual(tokens(), want) {
		t.Fatalf("call 1 used %q, want alice then bob", tokens())
	}
	if _, _, err := teamCall(e, context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if want := []string{"tok-alice", "tok-bob", "tok-bob"}; !reflect.DeepEqual(tokens(), want) {
		t.Fatalf("call 2 used %q, want bob only: alice is down", tokens())
	}

	limit("tok-alice", "tok-bob", "tok-carol")
	e.seats[0].down = time.Now().Add(-time.Second) // the reset of alice passed
	_, _, err := teamCall(e, context.Background(), req)
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Endpoint != EndpointClaudeTeam {
		t.Fatalf("call 3: %v; want every seat down", err)
	}
	if want := []string{"tok-alice", "tok-bob", "tok-bob", "tok-alice", "tok-bob", "tok-carol"}; !reflect.DeepEqual(tokens(), want) {
		t.Fatalf("call 3 used %q, want alice, bob and carol one time each", tokens())
	}
	for _, s := range []string{`seat "carol" reached its usage limit; it resets 3pm (Europe/Paris)`, "every claude seat (3) is at its usage limit; the first resets at"} {
		if !strings.Contains(err.Error(), s) {
			t.Errorf("error %q has no %q", err, s)
		}
	}
	if strings.Contains(err.Error(), "tok-") {
		t.Fatalf("the error shows a token: %v", err)
	}
	earliest := e.seats[0]
	for _, seat := range e.seats[1:] {
		if seat.down.Before(earliest.down) {
			earliest = seat
		}
	}
	if earliest.reset.IsZero() || !earliest.reset.Equal(earliest.down) || !ue.RetryAt.Equal(earliest.reset) {
		t.Fatalf("RetryAt %v, want the earliest reset %+v", ue.RetryAt, earliest)
	}
	// No seat is up: the call fails without a process.
	_, _, err = teamCall(e, context.Background(), req)
	if !errors.As(err, &ue) || !strings.HasPrefix(err.Error(), "LLM unavailable: every claude seat (3)") || len(tokens()) != 6 {
		t.Fatalf("call 4: %v with %d processes; want every seat down and no process", err, len(tokens()))
	}

	limit()
	e.seats[1].down = time.Now().Add(-time.Second) // the reset of bob passed
	if _, _, err := teamCall(e, context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if got := tokens(); got[len(got)-1] != "tok-bob" || len(got) != 7 {
		t.Fatalf("call 5 used %q, want bob", got)
	}
	if !strings.Contains(e.UsageReport(), "calls=5 failed=2") || !strings.Contains(e.UsageReport(), "seats (alice,bob,carol)") {
		t.Fatalf("report:\n%s", e.UsageReport())
	}
}

// A limit whose reset is not after now (a Unix time in the past, or no reset
// text) puts the seat down for claudeRetryDelay, and the call moves to the
// next seat.
func TestClaudeTeamEndpoint_RotationPastReset(t *testing.T) {
	for _, limit := range []string{
		`{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached|1760000000","usage":{}}`,
		`{"type":"result","subtype":"success","is_error":true,"result":"API Error: 429","api_error_status":429,"usage":{}}`,
	} {
		bin, dir := rotatingClaude(t, limit)
		if err := os.WriteFile(filepath.Join(dir, "limited"), []byte("tok-alice\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		e := &ClaudeTeamEndpoint{Binary: bin, seats: []claudeSeat{{name: "alice", token: "tok-alice"}, {name: "bob", token: "tok-bob"}}}
		start := time.Now()
		if _, _, err := teamCall(e, context.Background(), ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "q"}}}); err != nil {
			t.Fatalf("%s: %v; want the reply of bob", limit, err)
		}
		if got := readFile(t, filepath.Join(dir, "tokens")); got != "tok-alice\ntok-bob\n" {
			t.Fatalf("%s: used %q, want alice then bob", limit, got)
		}
		if down := e.seats[0].down; down.Before(start.Add(claudeRetryDelay)) || down.After(time.Now().Add(claudeRetryDelay)) {
			t.Fatalf("%s: alice is down until %v, want now + claudeRetryDelay", limit, down)
		}
	}
}

// Concurrent calls on a limited first seat all succeed on the next seat; the
// limited seat runs at most MaxProcs processes (the calls in flight when the
// first limit comes back).
func TestClaudeTeamEndpoint_RotationConcurrent(t *testing.T) {
	bin, dir := rotatingClaude(t, teamLimit)
	if err := os.WriteFile(filepath.Join(dir, "limited"), []byte("tok-alice\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e := &ClaudeTeamEndpoint{Binary: bin, MaxProcs: 4, seats: []claudeSeat{{name: "alice", token: "tok-alice"}, {name: "bob", token: "tok-bob"}}}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := teamCall(e, context.Background(), ChatRequest{MaxTokens: 10, Messages: []ChatMessage{{Role: "user", Content: "q"}}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	tokens := strings.Split(strings.TrimSpace(readFile(t, filepath.Join(dir, "tokens"))), "\n")
	alice, bob := 0, 0
	for _, tok := range tokens {
		switch tok {
		case "tok-alice":
			alice++
		case "tok-bob":
			bob++
		default:
			t.Fatalf("unknown token in %q", tokens)
		}
	}
	if alice < 1 || alice > 4 || bob != 8 {
		t.Fatalf("alice ran %d times, bob %d; want 1 to 4 and 8", alice, bob)
	}
}
