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
// Claude Code CLI) on a seat of the Team plan. A seat pays no token price: it
// has a usage limit per time window. A call goes to the first seat of
// CLAUDE_TEAM_PLAN_TOKENS that is not at its limit. When a seat reaches its
// limit, the seat is down until its reset time and the call moves to the next
// seat. When every seat is down, the call fails with ErrUnavailable and the
// earliest reset time.
type ClaudeTeamEndpoint struct {
	// Binary is the path of the CLI to run.
	Binary string
	// MaxProcs is the maximum number of CLI processes at one time. 0 means
	// defaultClaudeTeamProcs.
	MaxProcs int
	// CallTimeout bounds one CLI process. 0 means claudeTeamCallTimeout.
	CallTimeout time.Duration

	semOnce  sync.Once
	sem      chan struct{}
	mu       sync.Mutex
	seats    []claudeSeat // in the order of use; the tokens are never logged
	usage    map[usageKey]*TaskUsage
	prefixes prefixCache
}

const defaultClaudeTeamProcs = 4

// Concurrency is the number of CLI processes that run at one time
// (MaxProcs).
func (t *ClaudeTeamEndpoint) Concurrency() int {
	if t.MaxProcs > 0 {
		return t.MaxProcs
	}
	return defaultClaudeTeamProcs
}

// claudeTeamCallTimeout is the limit of one CLI process, the limit of a sync
// API request (claudeAPITimeout): a reply with a long thinking takes minutes.
// Without it, a CLI that hung held its caller: a stance merge waited 40 min
// on one call (2026-10-19).
const claudeTeamCallTimeout = claudeAPITimeout

// claudeTeamSystemPrompt is the system prompt of a request with no system
// message (the fact extractor sends its prompt as the user message). Without
// --system-prompt, the CLI loads its own long agent prompt, which costs
// tokens of the seat and changes the answers.
const claudeTeamSystemPrompt = "Follow the instructions of the user message."

// maxOutputSnippet is the length of the stderr or stdout part of an error.
const maxOutputSnippet = 500

// claudeSeat is one seat of the Team plan. A seat at its usage limit is down
// until down: its reset time, or now + unknownResetSkip when the CLI gives a
// reset text that does not parse (reset is then zero, and the error of the
// endpoint gives a zero RetryAt, as before the seats).
type claudeSeat struct {
	name  string
	token string    // the OAuth token of the seat; never logged
	down  time.Time // zero: the seat serves calls; else: the seat is down until this time
	reset time.Time // the reset time to report while the seat is down; zero: not known
}

// NewClaudeTeamEndpointFromEnv builds the endpoint of the seats of
// CLAUDE_TEAM_PLAN_TOKENS: name=token,name=token, in the order of use. When
// CLAUDE_TEAM_PLAN_SEAT names a seat, the order starts at that seat. The
// claude CLI must be on the PATH.
func NewClaudeTeamEndpointFromEnv() (*ClaudeTeamEndpoint, error) {
	tokens := os.Getenv("CLAUDE_TEAM_PLAN_TOKENS")
	if strings.TrimSpace(tokens) == "" {
		return nil, errors.New("CLAUDE_TEAM_PLAN_TOKENS environment variable not set")
	}
	seats, err := claudeSeats(tokens, strings.TrimSpace(os.Getenv("CLAUDE_TEAM_PLAN_SEAT")))
	if err != nil {
		return nil, err
	}
	bin, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("claude team endpoint: %w", err)
	}
	return &ClaudeTeamEndpoint{Binary: bin, seats: seats}, nil
}

// claudeSeats parses name=token,name=token into the seats, in the order of
// the list. A first seat ("" for none) moves to the front; the seats before it
// follow the others. The errors give the position of a bad entry, never its
// text: the text holds a token.
func claudeSeats(tokens, first string) ([]claudeSeat, error) {
	var seats []claudeSeat
	names := map[string]bool{}
	for i, entry := range strings.Split(tokens, ",") {
		name, token, ok := strings.Cut(entry, "=")
		name, token = strings.TrimSpace(name), strings.TrimSpace(token)
		if !ok || name == "" || token == "" {
			return nil, fmt.Errorf("CLAUDE_TEAM_PLAN_TOKENS: entry %d is not name=token", i+1)
		}
		if names[name] {
			return nil, fmt.Errorf("CLAUDE_TEAM_PLAN_TOKENS: seat %q is in the list 2 times", name)
		}
		names[name] = true
		seats = append(seats, claudeSeat{name: name, token: token})
	}
	if first == "" {
		return seats, nil
	}
	for i, seat := range seats {
		if seat.name == first {
			return append(seats[i:len(seats):len(seats)], seats[:i]...), nil
		}
	}
	return nil, fmt.Errorf("CLAUDE_TEAM_PLAN_SEAT: seat %q is not in CLAUDE_TEAM_PLAN_TOKENS", first)
}

// SeatNames returns the names of the seats, in the order of use. The names
// never change: no lock.
func (t *ClaudeTeamEndpoint) SeatNames() []string {
	names := make([]string, len(t.seats))
	for i := range t.seats {
		names[i] = t.seats[i].name
	}
	return names
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

// Call runs one CLI process for the call on a seat that is not at its limit
// and waits for its reply. Only an endpoint from NewClaudeTeamEndpointFromEnv
// has seats: a struct literal fails here.
func (t *ClaudeTeamEndpoint) Call(ctx context.Context, req ChatRequest, opts ClaudeOptions) (string, *ChatResponse, error) {
	if len(t.seats) == 0 {
		return "", nil, errors.New("claude team: no seat; use NewClaudeTeamEndpointFromEnv")
	}
	if opts.Effort == "" {
		return "", nil, fmt.Errorf("claude team: task %q has no effort", req.Task)
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
		return "", nil, fmt.Errorf("claude team: %w", ctx.Err())
	}
	defer func() { <-t.sem }()

	t.mu.Lock()
	u := countTaskCall(&t.usage, req.Task, creq.Model)
	t.mu.Unlock()
	text, res, err := t.callSeats(ctx, creq, system, prompt, req.wantsJSON())
	t.mu.Lock()
	u.Usage = u.Usage.Add(res.Usage)
	u.CostUSD += res.TotalCostUSD
	if err != nil {
		u.Failed++
	}
	t.mu.Unlock()
	if err != nil {
		return "", nil, err
	}
	return text, claudeChatResponse(text, res.Usage), nil
}

// callSeats runs the call on the first seat that is not down. A seat that
// reaches its usage limit is down until its reset time, and the call moves to
// the next seat. When every seat is down, the call fails with the
// UnavailableError of the earliest reset. The result is the one of the last
// process, zero when none ran.
func (t *ClaudeTeamEndpoint) callSeats(ctx context.Context, creq ClaudeRequest, system, prompt string, wantJSON bool) (string, claudeCLIResult, error) {
	var res claudeCLIResult
	var last *UnavailableError // the limit error of the last seat
	// One try per seat: a seat that fails here is down until a time after
	// now (answer), so pickSeat never returns it again in this call.
	for range t.seats {
		seat, earliest, ok := t.pickSeat(time.Now())
		if !ok {
			return "", res, t.unavailable(earliest, last)
		}
		var err error
		res, err = t.run(ctx, seat, creq, system, prompt)
		if err != nil {
			return "", res, err
		}
		text, err := t.answer(seat, res, wantJSON)
		if !errors.As(err, &last) {
			return text, res, err
		}
		now := time.Now()
		down := last.RetryAt
		if down.IsZero() {
			down = now.Add(unknownResetSkip)
		}
		t.seatDown(seat.name, down, last.RetryAt, now)
	}
	if _, earliest, ok := t.pickSeat(time.Now()); !ok {
		return "", res, t.unavailable(earliest, last)
	}
	return "", res, last // a seat came back during the call; the next call uses it
}

// pickSeat returns the first seat that is not down at now. When every seat is
// down, ok is false and earliest is the seat that comes back first.
func (t *ClaudeTeamEndpoint) pickSeat(now time.Time) (seat claudeSeat, earliest claudeSeat, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for i := range t.seats {
		seat := &t.seats[i]
		if seat.down.IsZero() || !now.Before(seat.down) {
			seat.down, seat.reset = time.Time{}, time.Time{}
			return *seat, claudeSeat{}, true
		}
		if earliest.down.IsZero() || seat.down.Before(earliest.down) {
			earliest = *seat
		}
	}
	return claudeSeat{}, earliest, false
}

// unavailable returns the error of a call when every seat is down, and
// earliest is the seat that comes back first: its reset is the RetryAt (zero
// when not known). cause is the limit error of the last seat that the call
// tried, nil when the call tried none.
func (t *ClaudeTeamEndpoint) unavailable(earliest claudeSeat, cause *UnavailableError) error {
	first := "at " + earliest.reset.Format(time.RFC3339)
	if earliest.reset.IsZero() {
		first = "at a time that is not known"
	}
	var err error
	if cause == nil {
		err = fmt.Errorf("%w: every claude seat (%d) is at its usage limit; the first resets %s",
			ErrUnavailable, len(t.seats), first)
	} else {
		err = fmt.Errorf("%w; every claude seat (%d) is at its usage limit; the first resets %s",
			cause.Err, len(t.seats), first)
	}
	return &UnavailableError{Endpoint: EndpointClaudeTeam, RetryAt: earliest.reset, Err: err}
}

// seatDown marks the seat named name as down until down, with reset as its
// reset time (zero when not known), and logs the first seat that is up at
// now ("" when every seat is down).
func (t *ClaudeTeamEndpoint) seatDown(name string, down, reset, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	next := ""
	for i := range t.seats {
		seat := &t.seats[i]
		if seat.name == name {
			seat.down, seat.reset = down, reset
		} else if next == "" && (seat.down.IsZero() || !now.Before(seat.down)) {
			next = seat.name
		}
	}
	log.Warn().Str("seat", name).Time("down_until", down).Str("next_seat", next).
		Msg("claude seat: usage limit reached; the seat is down until its reset")
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
	p := t.prefixes.next(family, text)
	t.mu.Unlock()
	if p == "" {
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

// run starts the CLI on seat with system as its system prompt and prompt on
// stdin, and decodes its result. The flags keep the local context of the CLI
// out of the call: without them, the CLI sent about 100,000 tokens of tools,
// settings and memory per call; with them, about 560 tokens.
func (t *ClaudeTeamEndpoint) run(ctx context.Context, seat claudeSeat, creq ClaudeRequest, system, prompt string) (claudeCLIResult, error) {
	limit := t.CallTimeout
	if limit <= 0 {
		limit = claudeTeamCallTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	cmd := exec.CommandContext(callCtx, t.Binary,
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
	cmd.Env = childEnv(seat.token, creq.MaxTokens)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.WaitDelay = claudeTeamWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	if err := ctx.Err(); err != nil {
		return claudeCLIResult{}, fmt.Errorf("claude seat %q: %w", seat.name, err)
	}
	if callCtx.Err() != nil {
		return claudeCLIResult{}, fmt.Errorf("claude seat %q: no reply within %s", seat.name, limit)
	}
	output := func() string {
		return fmt.Sprintf("(stderr: %s; stdout: %s)", outputSnippet(stderr.String()), outputSnippet(stdout.String()))
	}
	var res claudeCLIResult
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &res); err != nil || res.Type != "result" {
		if runErr != nil {
			return claudeCLIResult{}, fmt.Errorf("claude seat %q: %w %s", seat.name, runErr, output())
		}
		return claudeCLIResult{}, fmt.Errorf("claude seat %q: stdout is not a result object %s", seat.name, output())
	}
	if runErr != nil && !res.IsError {
		return claudeCLIResult{}, fmt.Errorf("claude seat %q: %w with a success result %s", seat.name, runErr, output())
	}
	return res, nil
}

// childEnv returns the environment of the CLI: the parent environment
// without the variables of the CLI and of the seats, plus the seat token
// token, the output limit and 2 fixed variables of the CLI. DISABLE_AUTOUPDATER=1 stops
// the CLI from updating the pinned binary. CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1
// stops its telemetry and other traffic that a call does not need. A parent
// variable can change the bill or leak the token:
//   - ANTHROPIC_API_KEY or ANTHROPIC_AUTH_TOKEN bills the API, not the seat.
//   - CLAUDE_CODE_USE_BEDROCK, _VERTEX or _FOUNDRY bills a cloud provider.
//   - ANTHROPIC_BASE_URL sends the seat token to another server.
//   - CLAUDECODE and CLAUDE_CODE_ENTRYPOINT or _SSE_PORT connect the child to
//     a parent session.
func childEnv(token string, maxTokens int) []string {
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
		"CLAUDE_CODE_OAUTH_TOKEN="+token,
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

// answer returns the text of a result of seat, or the error of the CLI. The
// wording of the usage-limit message of the CLI is not documented: the
// endpoint assumes HTTP status 429 or a text that matches claudeLimitText. A
// usage limit gives an UnavailableError whose RetryAt is the reset time of
// the seat: the parsed reset text, now + claudeRetryDelay when the text gives
// no reset or a reset that is not after now (a Unix time in the past), or
// zero when the reset text does not parse. RetryAt is thus never in the
// past: callSeats never tries the seat again in the same call.
func (t *ClaudeTeamEndpoint) answer(seat claudeSeat, res claudeCLIResult, wantJSON bool) (string, error) {
	if res.IsError || res.Subtype != "success" {
		if (res.APIErrorStatus != nil && *res.APIErrorStatus == 429) || claudeLimitText.MatchString(res.Result) {
			now := time.Now()
			reset, at := claudeReset(res.Result, now)
			switch {
			case reset != "" && at.IsZero():
				log.Warn().Str("seat", seat.name).Str("reset", reset).
					Msg("claude seat: the reset time does not parse")
			case !at.After(now):
				at = now.Add(claudeRetryDelay)
			}
			err := fmt.Errorf("%w: claude seat %q reached its usage limit: %s", ErrUnavailable, seat.name, outputSnippet(res.Result))
			if reset != "" {
				err = fmt.Errorf("%w: claude seat %q reached its usage limit; it resets %s", ErrUnavailable, seat.name, reset)
			}
			return "", &UnavailableError{Endpoint: EndpointClaudeTeam, RetryAt: at, Err: err}
		}
		return "", fmt.Errorf("claude seat %q: %s error: %s", seat.name, res.Subtype, outputSnippet(res.Result))
	}
	return claudeAnswer(fmt.Sprintf("claude seat %q", seat.name), res.StopReason, res.Result, res.Usage.OutputTokens, wantJSON)
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
// and cost. The cost is the list price that the CLI reports; the seats do not
// pay it.
func (t *ClaudeTeamEndpoint) UsageReport() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return usageReport(t.usage, false, "list price as the CLI reports it; the seats ("+strings.Join(t.SeatNames(), ",")+") do not pay it")
}

// outputSnippet returns the first maxOutputSnippet bytes of s.
func outputSnippet(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxOutputSnippet {
		return s[:maxOutputSnippet] + "..."
	}
	return s
}
