package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog/log"
)

// ClaudeTeamEndpoint answers each call with one `claude -p` process (the
// Claude Code CLI) on one seat of the Team plan. The seat pays no token price:
// it has a usage limit per time window. When the seat reaches its limit, a
// call fails with ErrUnavailable. The endpoint never changes to another seat:
// the seat changes only when the operator changes CLAUDE_TEAM_PLAN_SEAT.
type ClaudeTeamEndpoint struct {
	// Seat names the seat in CLAUDE_TEAM_PLAN_TOKENS. The errors name it.
	Seat string
	// Binary is the path of the CLI to run.
	Binary string
	// MaxProcs is the maximum number of CLI processes at one time. 0 means
	// defaultClaudeTeamProcs.
	MaxProcs int

	token string // the OAuth token of the seat; never logged

	semOnce  sync.Once
	sem      chan struct{}
	mu       sync.Mutex
	usage    map[usageKey]*TaskUsage
	prefixes prefixCache
	last     map[string]string // prompt family -> last prompt with no known prefix
}

const defaultClaudeTeamProcs = 4

// claudeTeamSystemPrompt is the system prompt of a request with no system
// message (the fact extractor sends its prompt as the user message). Without
// --system-prompt, the CLI loads its own long agent prompt, which costs
// tokens of the seat and changes the answers.
const claudeTeamSystemPrompt = "Follow the instructions of the user message."

// maxOutputSnippet is the length of the stderr or stdout part of an error.
const maxOutputSnippet = 500

// NewClaudeTeamEndpointFromEnv builds the endpoint of the seat that
// CLAUDE_TEAM_PLAN_SEAT names. CLAUDE_TEAM_PLAN_TOKENS holds the named tokens
// of the seats: name=token,name=token. The claude CLI must be on the PATH.
func NewClaudeTeamEndpointFromEnv() (*ClaudeTeamEndpoint, error) {
	seat := strings.TrimSpace(os.Getenv("CLAUDE_TEAM_PLAN_SEAT"))
	if seat == "" {
		return nil, errors.New("CLAUDE_TEAM_PLAN_SEAT environment variable not set")
	}
	tokens := os.Getenv("CLAUDE_TEAM_PLAN_TOKENS")
	if strings.TrimSpace(tokens) == "" {
		return nil, errors.New("CLAUDE_TEAM_PLAN_TOKENS environment variable not set")
	}
	token, err := claudeSeatToken(tokens, seat)
	if err != nil {
		return nil, err
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claude team endpoint: %w", err)
	}
	return &ClaudeTeamEndpoint{Seat: seat, Binary: bin, token: token}, nil
}

// claudeSeatToken parses name=token,name=token and returns the token of
// seat. The errors give the position of a bad entry, never its text: the
// text holds a token.
func claudeSeatToken(tokens, seat string) (string, error) {
	seats := map[string]string{}
	for i, entry := range strings.Split(tokens, ",") {
		name, token, ok := strings.Cut(entry, "=")
		name, token = strings.TrimSpace(name), strings.TrimSpace(token)
		if !ok || name == "" || token == "" {
			return "", fmt.Errorf("CLAUDE_TEAM_PLAN_TOKENS: entry %d is not name=token", i+1)
		}
		if _, dup := seats[name]; dup {
			return "", fmt.Errorf("CLAUDE_TEAM_PLAN_TOKENS: seat %q is in the list 2 times", name)
		}
		seats[name] = token
	}
	token, ok := seats[seat]
	if !ok {
		return "", fmt.Errorf("CLAUDE_TEAM_PLAN_SEAT: seat %q is not in CLAUDE_TEAM_PLAN_TOKENS", seat)
	}
	return token, nil
}

// claudeCLIResult is the JSON object that `claude -p --output-format json`
// writes on stdout.
type claudeCLIResult struct {
	Type           string      `json:"type"`
	Subtype        string      `json:"subtype"`
	IsError        bool        `json:"is_error"`
	StopReason     string      `json:"stop_reason"`
	Result         string      `json:"result"`
	APIErrorStatus *int        `json:"api_error_status"`
	TotalCostUSD   float64     `json:"total_cost_usd"`
	Usage          ClaudeUsage `json:"usage"`
}

// Call runs one CLI process for the call and waits for its reply. Only an
// endpoint from NewClaudeTeamEndpointFromEnv has a token: a struct literal
// fails here.
func (t *ClaudeTeamEndpoint) Call(ctx context.Context, req ChatRequest, opts ClaudeOptions) (string, *ChatResponse, error) {
	if t.token == "" {
		return "", nil, fmt.Errorf("claude seat %q: no token; use NewClaudeTeamEndpointFromEnv", t.Seat)
	}
	if opts.Effort == "" {
		return "", nil, fmt.Errorf("claude seat %q: task %q has no effort", t.Seat, req.Task)
	}
	creq, err := NewClaudeRequest(req, opts)
	if err != nil {
		return "", nil, err
	}
	prompt, err := claudeTeamPrompt(creq.Messages)
	if err != nil {
		return "", nil, err
	}
	// The system blocks join as in ClaudeRequest.canonical.
	system := joinBlocks(creq.System)
	if system == "" {
		system = claudeTeamSystemPrompt
	}
	if p := t.cachePrefix(req.Task, creq); p != "" {
		system += "\n\n" + p
		prompt = prompt[len(p):]
	}
	t.semOnce.Do(func() {
		n := t.MaxProcs
		if n <= 0 {
			n = defaultClaudeTeamProcs
		}
		t.sem = make(chan struct{}, n)
	})
	select {
	case t.sem <- struct{}{}:
	case <-ctx.Done():
		return "", nil, fmt.Errorf("claude seat %q: %w", t.Seat, ctx.Err())
	}
	defer func() { <-t.sem }()

	t.mu.Lock()
	u := countTaskCall(&t.usage, req.Task, creq.Model)
	t.mu.Unlock()
	res, err := t.run(ctx, creq, system, prompt)
	var text string
	if err == nil {
		t.mu.Lock()
		u.Usage = u.Usage.Add(res.Usage)
		u.CostUSD += res.TotalCostUSD
		t.mu.Unlock()
		text, err = t.answer(res, req.wantsJSON())
	}
	if err != nil {
		t.mu.Lock()
		u.Failed++
		t.mu.Unlock()
		return "", nil, err
	}
	return text, claudeChatResponse(text, res.Usage), nil
}

// cachePrefix returns the learned static prefix of the prompt of creq, or "".
// The CLI puts its cache breakpoint at the end of the user message. A prompt
// that starts with its static rules thus writes the whole prompt to the cache
// on each call and reads nothing. Call sends the prefix in --system-prompt,
// so the next call of the family reads it from the cache. The endpoint gets
// the calls one at a time: a family with no known prefix learns one from its
// last prompt. cachePrefix splits only a request with one user message: a
// conversation goes as one labelled prompt (claudeTeamPrompt).
func (t *ClaudeTeamEndpoint) cachePrefix(task Task, creq ClaudeRequest) string {
	if len(creq.Messages) != 1 {
		return ""
	}
	family, text, ok := promptFamily(task, creq)
	if !ok {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	p := t.prefixes.known(family, text)
	if p == "" {
		if prev, seen := t.last[family]; seen {
			p = t.prefixes.learn(family, []string{prev, text})
		}
	}
	if p == "" {
		if t.last == nil {
			t.last = map[string]string{}
		}
		t.last[family] = text
		return ""
	}
	// Two equal prompts (the votes of a judge) can learn the whole prompt.
	// The CLI needs a user message, so the prompt then goes whole on stdin.
	if strings.TrimSpace(text[len(p):]) == "" {
		return ""
	}
	return p
}

// claudeTeamWaitDelay is the time that Run waits for the pipes after the
// process ends or is killed. A child process of the CLI that keeps a pipe
// open cannot then hold a cancelled call.
const claudeTeamWaitDelay = 5 * time.Second

// run starts the CLI with system as its system prompt and prompt on stdin,
// and decodes its result. The flags keep the local context of the CLI out of
// the call: without them, the CLI sent about 100,000 tokens of tools,
// settings and memory per call; with them, about 560 tokens.
func (t *ClaudeTeamEndpoint) run(ctx context.Context, creq ClaudeRequest, system, prompt string) (claudeCLIResult, error) {
	cmd := exec.CommandContext(ctx, t.Binary,
		"-p",
		"--output-format", "json",
		"--model", creq.Model,
		"--effort", string(creq.OutputConfig.Effort),
		"--system-prompt", system,
		"--tools", "",
		"--no-session-persistence",
		"--strict-mcp-config",
		"--setting-sources", "",
		"--disable-slash-commands",
		"--exclude-dynamic-system-prompt-sections",
	)
	cmd.Env = t.childEnv(creq.MaxTokens)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.WaitDelay = claudeTeamWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if err := ctx.Err(); err != nil {
		return claudeCLIResult{}, fmt.Errorf("claude seat %q: %w", t.Seat, err)
	}
	output := func() string {
		return fmt.Sprintf("(stderr: %s; stdout: %s)", outputSnippet(stderr.String()), outputSnippet(stdout.String()))
	}
	var res claudeCLIResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil || res.Type != "result" {
		if runErr != nil {
			return claudeCLIResult{}, fmt.Errorf("claude seat %q: %w %s", t.Seat, runErr, output())
		}
		return claudeCLIResult{}, fmt.Errorf("claude seat %q: stdout is not a result object %s", t.Seat, output())
	}
	if runErr != nil && !res.IsError {
		return claudeCLIResult{}, fmt.Errorf("claude seat %q: %w with a success result %s", t.Seat, runErr, output())
	}
	return res, nil
}

// childEnv returns the environment of the CLI: the parent environment
// without the variables of the CLI and of the seats, plus the seat token, the
// output limit and 2 fixed variables of the CLI. DISABLE_AUTOUPDATER=1 stops
// the CLI from updating the pinned binary. CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
// stops its telemetry and other traffic that a call does not need. A parent
// variable can change the bill or leak the token:
//   - ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN bills the API, not the seat.
//   - CLAUDE_CODE_USE_BEDROCK, _VERTEX or _FOUNDRY bills a cloud provider.
//   - ANTHROPIC_BASE_URL sends the seat token to another server.
//   - CLAUDECODE and CLAUDE_CODE_ENTRYPOINT or _SSE_PORT connect the child to
//     a parent session.
func (t *ClaudeTeamEndpoint) childEnv(maxTokens int) []string {
	var env []string
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "ANTHROPIC_") || strings.HasPrefix(name, "CLAUDE_CODE_") ||
			strings.HasPrefix(name, "CLAUDECODE") || strings.HasPrefix(name, "CLAUDE_TEAM_PLAN_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env,
		"CLAUDE_CODE_OAUTH_TOKEN="+t.token,
		"CLAUDE_CODE_MAX_OUTPUT_TOKENS="+strconv.Itoa(maxTokens),
		"DISABLE_AUTOUPDATER=1",
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
	)
}

// claudeTeamPrompt returns the stdin of the CLI. The CLI takes one prompt,
// so a conversation (the corrective retry of VerdictJudge, with the bad
// reply as an assistant turn) goes as one user message: each turn under a
// label line. The last turn must be a user turn: the CLI cannot continue an
// assistant turn.
func claudeTeamPrompt(messages []claudeMessage) (string, error) {
	if last := messages[len(messages)-1]; last.Role != "user" {
		return "", fmt.Errorf("claude team prompt: the last turn is %q, not user", last.Role)
	}
	if len(messages) == 1 {
		return joinBlocks(messages[0].Content), nil
	}
	var b strings.Builder
	b.WriteString("This is a conversation. Each turn starts with a label line. Reply to the last USER turn.\n")
	for _, m := range messages {
		label := "=== USER ==="
		if m.Role == "assistant" {
			label = "=== ASSISTANT (your previous reply) ==="
		}
		fmt.Fprintf(&b, "\n%s\n%s\n", label, joinBlocks(m.Content))
	}
	return b.String(), nil
}

// answer returns the text of a result, or the error of the CLI. The wording
// of the usage-limit message of the CLI is not documented: the endpoint
// assumes HTTP status 429 or a text that matches claudeLimitText.
func (t *ClaudeTeamEndpoint) answer(res claudeCLIResult, wantJSON bool) (string, error) {
	if res.IsError || res.Subtype != "success" {
		if (res.APIErrorStatus != nil && *res.APIErrorStatus == 429) || claudeLimitText.MatchString(res.Result) {
			// No reset text: retry after claudeRetryDelay. A reset text that
			// does not parse: RetryAt stays zero, and the run does not wait.
			reset, at := claudeReset(res.Result, time.Now())
			switch {
			case reset == "":
				at = time.Now().Add(claudeRetryDelay)
			case at.IsZero():
				log.Warn().Str("seat", t.Seat).Str("reset", reset).
					Msg("claude seat: the reset time does not parse; the run will not wait for it")
			}
			err := fmt.Errorf("%w: claude seat %q reached its usage limit: %s", ErrUnavailable, t.Seat, outputSnippet(res.Result))
			if reset != "" {
				err = fmt.Errorf("%w: claude seat %q reached its usage limit; it resets %s", ErrUnavailable, t.Seat, reset)
			}
			return "", &UnavailableError{Endpoint: EndpointClaudeTeam, RetryAt: at, Err: err}
		}
		return "", fmt.Errorf("claude seat %q: %s error: %s", t.Seat, res.Subtype, outputSnippet(res.Result))
	}
	return claudeAnswer(fmt.Sprintf("claude seat %q", t.Seat), res.StopReason, res.Result, res.Usage.OutputTokens, wantJSON)
}

var (
	// claudeLimitText finds a usage limit of the seat. A context limit is
	// not a usage limit: it does not match.
	claudeLimitText = regexp.MustCompile(`(?i)usage limit|hit your limit`)
	// claudeResetText finds the reset time in "resets 3pm (Europe/Paris)" or
	// "resets at 3.30pm.": the rest of the line, without a final period.
	claudeResetText = regexp.MustCompile(`(?im)resets?\s+(?:at\s+)?(.+?)\.?$`)
	// claudeResetUnix finds the Unix time after a "|", as in
	// "Claude AI usage limit reached|1760000000".
	claudeResetUnix = regexp.MustCompile(`\|(\d{10})\b`)
)

// claudeClockText finds the reset time in a reset text, as in "3pm",
// "3.30pm", "3:30pm (Europe/Paris)" or "Oct 16, 9am (Europe/Paris)": an
// optional date, the clock time and an optional time zone.
var claudeClockText = regexp.MustCompile(`(?i)^(?:([a-z]{3})[a-z]*\.?\s+(\d{1,2}),?\s+(?:at\s+)?)?(\d{1,2})(?:[:.](\d{2}))?\s*(am|pm)\b(?:\s*\(([^)]+)\))?`)

// claudeResetMargin is added to a parsed reset time: the seat must be reset
// when the next call starts.
const claudeResetMargin = time.Minute

// claudeResetSkew is how long a reset time can be in the past and still be
// the current reset (clock skew, a time rounded down by the CLI). A reset in
// this window gives now + claudeRetryDelay. An older reset is the next one:
// the next day, or the next year for a text with a date.
const claudeResetSkew = time.Hour

// claudeReset returns the reset time that text gives: reset is the text for
// the error, "" when text gives none, and at is the time of the next call,
// zero when reset does not parse. A time without a time zone is in the local
// zone: the CLI runs on the same host and prints its local time.
func claudeReset(text string, now time.Time) (reset string, at time.Time) {
	if m := claudeResetUnix.FindStringSubmatch(text); m != nil {
		sec, _ := strconv.ParseInt(m[1], 10, 64) // 10 digits always parse
		at = time.Unix(sec, 0).UTC()
		return "at " + at.Format(time.RFC3339), at.Add(claudeResetMargin)
	}
	m := claudeResetText.FindStringSubmatch(text)
	if m == nil {
		return "", time.Time{}
	}
	reset = strings.TrimSpace(m[1])
	c := claudeClockText.FindStringSubmatch(reset)
	if c == nil {
		return reset, time.Time{}
	}
	hour, _ := strconv.Atoi(c[3])   // 1 or 2 digits always parse
	minute, _ := strconv.Atoi(c[4]) // no minutes ("") gives 0
	if hour < 1 || hour > 12 || minute > 59 {
		return reset, time.Time{}
	}
	hour %= 12
	if strings.EqualFold(c[5], "pm") {
		hour += 12
	}
	loc := time.Local
	if c[6] != "" {
		var err error
		if loc, err = time.LoadLocation(c[6]); err != nil {
			return reset, time.Time{}
		}
	}
	local := now.In(loc)
	year, month, day := local.Date()
	if c[1] != "" {
		mon, err := time.Parse("Jan", strings.ToUpper(c[1][:1])+strings.ToLower(c[1][1:]))
		if err != nil {
			return reset, time.Time{}
		}
		month = mon.Month()
		day, _ = strconv.Atoi(c[2]) // 1 or 2 digits always parse
		if day < 1 || day > 31 {
			return reset, time.Time{}
		}
	}
	at = time.Date(year, month, day, hour, minute, 0, 0, loc)
	if at.Day() != day {
		return reset, time.Time{} // a day that the month does not have
	}
	switch {
	case at.After(now):
	case now.Sub(at) <= claudeResetSkew:
		return reset, now.Add(claudeRetryDelay)
	case c[1] != "":
		at = at.AddDate(1, 0, 0)
	default:
		at = at.AddDate(0, 0, 1)
	}
	return reset, at.Add(claudeResetMargin)
}

// UsageReport returns one line per task and model: calls, failures, tokens
// and cost. The cost is the list price that the CLI reports; the seat does not pay it.
func (t *ClaudeTeamEndpoint) UsageReport() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return usageReport(t.usage, false, "list price as the CLI reports it; the seat does not pay it")
}

// outputSnippet returns the first maxOutputSnippet bytes of s.
func outputSnippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxOutputSnippet {
		return s[:maxOutputSnippet] + "..."
	}
	return s
}
