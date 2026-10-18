package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// fakeMessagesAPI mimics POST /messages. It records each request body and
// answers with status and body. A status of 0 answers 200 with apiReply.
// When hold is set, a request signals arrived and waits for hold to close, or
// for the client to give up, before it answers.
type fakeMessagesAPI struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	bodies  []ClaudeRequest
	status  int
	body    string
	hold    chan struct{}
	arrived chan struct{}
}

const apiReply = `{"id":"msg_1","model":"claude-sonnet-5-5","stop_reason":"end_turn",` +
	`"content":[{"type":"thinking","thinking":"hm"},{"type":"text","text":"` + "```json\\n{\\\"ok\\\":true}\\n```" + `"}],` +
	`"usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":3000,"cache_read_input_tokens":0,` +
	`"cache_creation":{"ephemeral_5m_input_tokens":3000,"ephemeral_1h_input_tokens":0}}}`

// apiReplyCost is the cost of apiReply on Sonnet 5.5: 100 input, 50 output
// and 3000 tokens written to the 5 min cache.
const apiReplyCost = (100*2 + 50*10 + 3000*2.5) / 1e6

func newFakeMessagesAPI(t *testing.T) *fakeMessagesAPI {
	f := &fakeMessagesAPI{t: t}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/messages" {
			t.Errorf("request %s %s, want POST /messages", r.Method, r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "k" || r.Header.Get("anthropic-version") != claudeAPIVersion ||
			r.Header.Get("content-type") != "application/json" {
			t.Errorf("headers %v", r.Header)
		}
		data, _ := io.ReadAll(r.Body)
		var creq ClaudeRequest
		if err := json.Unmarshal(data, &creq); err != nil {
			t.Errorf("decode request: %v", err)
		}
		f.mu.Lock()
		f.bodies = append(f.bodies, creq)
		status, body, hold := f.status, f.body, f.hold
		f.mu.Unlock()
		if hold != nil {
			f.arrived <- struct{}{}
			select {
			case <-hold:
			case <-r.Context().Done():
				return
			}
		}
		if status == 0 {
			status, body = http.StatusOK, apiReply
		}
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeMessagesAPI) endpoint() *ClaudeAPIEndpoint {
	return &ClaudeAPIEndpoint{
		api:    claudeHTTP{httpClient: &http.Client{Timeout: 2 * time.Second}, baseURL: f.srv.URL, key: "k"},
		maxUSD: claudeAPIRunCapUSD,
	}
}

func (f *fakeMessagesAPI) requests() []ClaudeRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]ClaudeRequest(nil), f.bodies...)
}

// apiOpts are the Claude settings of the calls of the API tests.
var apiOpts = ClaudeOptions{Model: "claude-sonnet-5-5", Effort: EffortMedium, ThinkingTokens: 1000}

// rulesPrompt returns a prompt with 3000 bytes of static rules and a
// variable tail.
func rulesPrompt(tail string) string {
	return strings.Repeat("rule line of the family.\n", 120) + tail
}

func TestClaudeAPIEndpoint_Call(t *testing.T) {
	f := newFakeMessagesAPI(t)
	e := f.endpoint()
	req := func(tail string) ChatRequest {
		return ChatRequest{
			Messages:       []ChatMessage{{Role: "system", Content: "sys"}, {Role: "user", Content: rulesPrompt(tail)}},
			MaxTokens:      200,
			Task:           TaskDetect,
			ResponseFormat: &ResponseFormat{Type: "json_object"},
		}
	}
	content, resp, err := e.Call(context.Background(), req("pair 1"), apiOpts)
	if err != nil || content != `{"ok":true}` {
		t.Fatalf("call 1: %q %v", content, err)
	}
	if resp.Usage.PromptTokens != 3100 || resp.Usage.CompletionTokens != 50 {
		t.Fatalf("usage %+v", resp.Usage)
	}
	if _, _, err := e.Call(context.Background(), req("pair 2"), apiOpts); err != nil {
		t.Fatal(err)
	}
	got := f.requests()
	if len(got) != 2 {
		t.Fatalf("%d requests, want 2", len(got))
	}
	first := got[0]
	if first.Model != "claude-sonnet-5-5" || first.MaxTokens != 1200 || first.Thinking == nil ||
		first.OutputConfig == nil || first.OutputConfig.Effort != EffortMedium || joinBlocks(first.System) != "sys" {
		t.Fatalf("request 1: %+v", first)
	}
	if first.hasCacheBreakpoint() {
		t.Fatal("the first call of a family has no learned prefix and no breakpoint")
	}
	blocks := got[1].Messages[0].Content
	if len(blocks) != 2 || blocks[0].CacheControl == nil || blocks[0].CacheControl.Type != "ephemeral" ||
		blocks[0].CacheControl.TTL != claudeAPICacheTTL || blocks[0].Text != rulesPrompt("") || blocks[1].Text != "pair 2" {
		t.Fatalf("the second call must cache the rules of the family, blocks %+v", blocks)
	}
	if math.Abs(e.spentUSD-2*apiReplyCost) > 1e-12 {
		t.Fatalf("spent %.6f USD, want %.6f", e.spentUSD, 2*apiReplyCost)
	}
	if report := e.UsageReport(); !strings.Contains(report, "detect          claude-sonnet-5-5 calls=2 failed=0 uncached=1 input=200 cache_write=6000") {
		t.Fatalf("report:\n%s", report)
	}
}

func TestClaudeAPIEndpoint_NeedsTheKey(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "")
	if _, err := NewClaudeAPIEndpointFromEnv(); err == nil {
		t.Fatal("want an error without ANTHROPIC_API_KEY")
	}
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10}
	if _, _, err := (&ClaudeAPIEndpoint{}).Call(context.Background(), req, apiOpts); err == nil {
		t.Fatal("an endpoint without a key must fail")
	}
}

// wantStopped checks that err stops the endpoint: an UnavailableError of the
// API endpoint with retryAt as its RetryAt (zero when not known).
func wantStopped(t *testing.T, err error, retryAt time.Time) {
	t.Helper()
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Endpoint != EndpointClaudeAPI || !ue.RetryAt.Equal(retryAt) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("err = %v, want an UnavailableError of %s with RetryAt %s", err, EndpointClaudeAPI, retryAt)
	}
}

// nextDay is the start of the next UTC day: the RetryAt of a stop by the cap.
func nextDay() time.Time {
	return time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, 1)
}

// apiReserve returns the worst-case cost that the endpoint reserves for req.
func apiReserve(t *testing.T, req ChatRequest) float64 {
	t.Helper()
	creq, err := NewClaudeRequest(req, apiOpts)
	if err != nil {
		t.Fatal(err)
	}
	c, err := worstCaseCostUSD(creq, false)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClaudeAPIEndpoint_SpendCapStopsTheRun(t *testing.T) {
	var alerts []string
	OnCreditError = func(provider, msg string) { alerts = append(alerts, provider) }
	t.Cleanup(func() { OnCreditError = nil })
	f := newFakeMessagesAPI(t)
	e := f.endpoint()
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskAudit}
	reserve := apiReserve(t, req)
	if reserve <= apiReplyCost {
		t.Fatalf("the worst case %.6f USD must be above the real cost %.6f USD", reserve, apiReplyCost)
	}
	// Call 2 starts at 1 real cost + its worst case = the cap; call 3 passes it.
	e.maxUSD = apiReplyCost + reserve
	for i := 0; i < 2; i++ {
		if _, _, err := e.Call(context.Background(), req, apiOpts); err != nil {
			t.Fatalf("call %d is under the cap: %v", i+1, err)
		}
	}
	if math.Abs(e.spentUSD-2*apiReplyCost) > 1e-12 {
		t.Fatalf("spent %.6f USD, want the real cost of 2 replies %.6f USD", e.spentUSD, 2*apiReplyCost)
	}
	for i := 0; i < 2; i++ {
		_, _, err := e.Call(context.Background(), req, apiOpts)
		wantStopped(t, err, nextDay())
	}
	if n := len(f.requests()); n != 2 {
		t.Fatalf("%d requests, want 2: a call past the cap must send nothing", n)
	}
	if len(alerts) != 1 || alerts[0] != "claude-api" {
		t.Fatalf("alerts %q, want 1 credit alert of claude-api", alerts)
	}
}

// Concurrent calls reserve their worst case before they send: the cap holds
// while no reply has come back.
func TestClaudeAPIEndpoint_SpendCapHoldsForConcurrentCalls(t *testing.T) {
	f := newFakeMessagesAPI(t)
	f.hold, f.arrived = make(chan struct{}), make(chan struct{}, 5)
	e := f.endpoint()
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskAudit}
	e.maxUSD = 3.5 * apiReserve(t, req)
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		go func() {
			_, _, err := e.Call(context.Background(), req, apiOpts)
			errs <- err
		}()
	}
	for i := 0; i < 2; i++ {
		wantStopped(t, <-errs, nextDay()) // 2 calls pass the cap at once
	}
	for i := 0; i < 3; i++ {
		<-f.arrived
	}
	close(f.hold)
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("a call under the cap failed: %v", err)
		}
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("%d requests, want 3", n)
	}
}

// A rejected request costs nothing; a call that gets no reply keeps its
// reservation: the API can have billed it.
func TestClaudeAPIEndpoint_Reservation(t *testing.T) {
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskAudit}
	f := newFakeMessagesAPI(t)
	f.status, f.body = http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error"}}`
	e := f.endpoint()
	if _, _, err := e.Call(context.Background(), req, apiOpts); err == nil {
		t.Fatal("want the 400")
	}
	if e.spentUSD != 0 {
		t.Fatalf("a rejected request spent %.6f USD, want 0", e.spentUSD)
	}

	f = newFakeMessagesAPI(t)
	f.hold, f.arrived = make(chan struct{}), make(chan struct{}, 1)
	e = f.endpoint()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-f.arrived
		cancel()
	}()
	if _, _, err := e.Call(ctx, req, apiOpts); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancel", err)
	}
	if want := apiReserve(t, req); math.Abs(e.spentUSD-want) > 1e-12 {
		t.Fatalf("a call without a reply spent %.6f USD, want its reservation %.6f USD", e.spentUSD, want)
	}
}

// A reply that stopped at max_tokens or refused is billed: its cost counts.
func TestClaudeAPIEndpoint_FailedReplyIsCounted(t *testing.T) {
	for _, stop := range []string{"max_tokens", "refusal"} {
		f := newFakeMessagesAPI(t)
		f.status, f.body = http.StatusOK, strings.Replace(apiReply, `"end_turn"`, `"`+stop+`"`, 1)
		e := f.endpoint()
		req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskAudit}
		if _, _, err := e.Call(context.Background(), req, apiOpts); err == nil || !strings.Contains(err.Error(), stop) {
			t.Fatalf("%s: want an error, got %v", stop, err)
		}
		if math.Abs(e.spentUSD-apiReplyCost) > 1e-12 || !strings.Contains(e.UsageReport(), "calls=1 failed=1") ||
			!strings.Contains(e.UsageReport(), "cost=$0.0082") {
			t.Fatalf("%s: spent %.6f USD, report:\n%s", stop, e.spentUSD, e.UsageReport())
		}
	}
}

func TestClaudeAPIEndpoint_CreditErrorStopsTheRun(t *testing.T) {
	for name, c := range map[string]struct {
		status int
		body   string
	}{
		"402":         {http.StatusPaymentRequired, `{"type":"error","error":{"type":"billing_error","message":"no credit"}}`},
		"spend limit": {http.StatusTooManyRequests, `{"type":"error","error":{"type":"rate_limit_error","details":{"error_code":"enforced_spend_limit_reached"}}}`},
		"401":         {http.StatusUnauthorized, `{"type":"error","error":{"type":"authentication_error"}}`},
		"403":         {http.StatusForbidden, `{"type":"error","error":{"type":"permission_error"}}`},
		"404":         {http.StatusNotFound, `{"type":"error","error":{"type":"not_found_error"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			var credit string
			OnCreditError = func(provider, msg string) { credit = provider }
			t.Cleanup(func() { OnCreditError = nil })
			f := newFakeMessagesAPI(t)
			f.status, f.body = c.status, c.body
			e := f.endpoint()
			req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskAudit}
			_, _, err := e.Call(context.Background(), req, apiOpts)
			wantStopped(t, err, time.Time{})
			wantAlert := "claude" // the credit alert of do
			if c.status != http.StatusPaymentRequired && c.status != http.StatusTooManyRequests {
				wantAlert = ""
			}
			if credit != wantAlert {
				t.Fatalf("credit alert %q, want %q", credit, wantAlert)
			}
			_, _, err = e.Call(context.Background(), req, apiOpts)
			wantStopped(t, err, time.Time{})
			if n := len(f.requests()); n != 1 {
				t.Fatalf("%d requests, want 1: a stopped endpoint must send nothing", n)
			}
		})
	}
}

func TestClaudeAPIEndpoint_ErrorKinds(t *testing.T) {
	f := newFakeMessagesAPI(t)
	f.status, f.body = http.StatusBadRequest, `{"type":"error","error":{"type":"invalid_request_error"}}`
	e := f.endpoint()
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskAudit}
	_, _, err := e.Call(context.Background(), req, apiOpts)
	var ce *ClientError
	var ue *UnavailableError
	if !errors.As(err, &ce) || errors.As(err, &ue) {
		t.Fatalf("a 400 is a plain ClientError, got %v", err)
	}
	// A transport error or a 5xx after the retries (2 min) is an outage of
	// the API, with a retry time.
	err = e.callError(context.Background(), errors.New("claude server error: 529"))
	if !errors.As(err, &ue) || ue.Endpoint != EndpointClaudeAPI || ue.RetryAt.IsZero() {
		t.Fatalf("an outage must give an UnavailableError with a RetryAt, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.callError(ctx, context.Canceled); errors.As(err, &ue) {
		t.Fatalf("a cancelled call is not an outage, got %v", err)
	}
}

// fallbackRouting routes every task to the seat of bin, with the API of f as
// the fallback.
func fallbackRouting(t *testing.T, bin string, f *fakeMessagesAPI) *Routing {
	t.Helper()
	r := ClaudeRouting(EndpointClaudeTeam, teamEndpoint(bin), "claude-sonnet-5-5")
	r.Endpoints[EndpointClaudeAPI] = f.endpoint()
	r.Default.Fallback = EndpointClaudeAPI
	for task, rt := range r.Tasks {
		rt.Fallback = EndpointClaudeAPI
		r.Tasks[task] = rt
	}
	return r
}

const seatLimited = `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit · resets 3pm (Europe/Paris)","usage":{}}`

func TestChatFallsBackOnASeatLimit(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&logs)
	t.Cleanup(func() { log.Logger = prev })

	c, server := chatTestClient(t, 200, `{"choices":[{"message":{"role":"assistant","content":"server"},"finish_reason":"stop"}]}`)
	bin, _ := fakeClaude(t, seatLimited, "", 1)
	f := newFakeMessagesAPI(t)
	setRouting(t, fallbackRouting(t, bin, f))
	for _, task := range []Task{TaskDetect, TaskDetect, TaskAudit} {
		req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: task,
			ResponseFormat: &ResponseFormat{Type: "json_object"}}
		content, _, err := c.Chat(context.Background(), req)
		if err != nil || content != `{"ok":true}` {
			t.Fatalf("%s: want the reply of the API, got %q %v", task, content, err)
		}
	}
	got := f.requests()
	if len(got) != 3 || *server != nil {
		t.Fatalf("%d API requests (want 3); the local server got %s", len(got), *server)
	}
	if got[0].Model != "claude-sonnet-5-5" || got[0].OutputConfig.Effort != EffortMedium || got[2].OutputConfig.Effort != EffortLow {
		t.Fatalf("the fallback must get the model and the effort of the route: %+v %+v", got[0], got[2])
	}
	if n := strings.Count(logs.String(), "the calls of the task go to the fallback"); n != 2 {
		t.Fatalf("%d switch warnings, want 1 per task (2):\n%s", n, logs.String())
	}
}

func TestChatDoesNotFallBackOnAPlainError(t *testing.T) {
	c, _ := chatTestClient(t, 200, `{}`)
	bin, _ := fakeClaude(t, `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"boom","usage":{}}`, "", 1)
	f := newFakeMessagesAPI(t)
	setRouting(t, fallbackRouting(t, bin, f))
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskDetect}
	_, _, err := c.Chat(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "boom") || errors.Is(err, ErrUnavailable) {
		t.Fatalf("want the error of the seat, got %v", err)
	}
	if n := len(f.requests()); n != 0 {
		t.Fatalf("a plain error must not go to the fallback, %d API requests", n)
	}
}

// The seat is at its limit with a known reset and the API is at its cap: the
// error is the one of the seat, so the gate waits for the reset instead of
// stopping the run. The message names both failures.
func TestChatReturnsTheErrorOfTheFallback(t *testing.T) {
	c, _ := chatTestClient(t, 200, `{}`)
	reset := time.Now().Add(2 * time.Hour).Unix()
	bin, _ := fakeClaude(t, fmt.Sprintf(`{"type":"result","subtype":"success","is_error":true,"result":"Claude AI usage limit reached|%d","usage":{}}`, reset), "", 1)
	f := newFakeMessagesAPI(t)
	r := fallbackRouting(t, bin, f)
	r.Endpoints[EndpointClaudeAPI].(*ClaudeAPIEndpoint).maxUSD = 0
	setRouting(t, r)
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskDetect}
	_, _, err := c.Chat(context.Background(), req)
	seatReset := time.Unix(reset, 0).Add(claudeResetMargin)
	var ue *UnavailableError
	if !errors.As(err, &ue) || ue.Endpoint != EndpointClaudeTeam || !ue.RetryAt.Equal(seatReset) {
		t.Fatalf("err = %v, want the UnavailableError of the seat with RetryAt %v", err, seatReset)
	}
	if !strings.Contains(err.Error(), "usage limit") || !strings.Contains(err.Error(), "passes the cap") {
		t.Fatalf("the error must name both failures: %v", err)
	}
}

func TestBothFailedTakesTheEarliestRetryAt(t *testing.T) {
	rt := Route{Endpoint: EndpointClaudeTeam, Fallback: EndpointClaudeAPI}
	now := time.Now()
	unavailable := func(e EndpointName, at time.Time) error {
		return &UnavailableError{Endpoint: e, RetryAt: at, Err: fmt.Errorf("%w: %s down", ErrUnavailable, e)}
	}
	for name, c := range map[string]struct {
		primary, fallback error
		want              EndpointName
		wantAt            time.Time
	}{
		"fallback stopped":     {unavailable(EndpointClaudeTeam, now.Add(time.Hour)), unavailable(EndpointClaudeAPI, time.Time{}), EndpointClaudeTeam, now.Add(time.Hour)},
		"fallback back first":  {unavailable(EndpointClaudeTeam, now.Add(time.Hour)), unavailable(EndpointClaudeAPI, now.Add(time.Minute)), EndpointClaudeAPI, now.Add(time.Minute)},
		"primary back first":   {unavailable(EndpointClaudeTeam, now.Add(time.Minute)), unavailable(EndpointClaudeAPI, now.Add(time.Hour)), EndpointClaudeTeam, now.Add(time.Minute)},
		"both reset not known": {unavailable(EndpointClaudeTeam, time.Time{}), unavailable(EndpointClaudeAPI, time.Time{}), EndpointClaudeAPI, time.Time{}},
	} {
		err := bothFailed(rt, c.primary, c.fallback)
		var ue *UnavailableError
		if !errors.As(err, &ue) || ue.Endpoint != c.want || !ue.RetryAt.Equal(c.wantAt) || !errors.Is(err, ErrUnavailable) ||
			!strings.Contains(err.Error(), "claude-team down") || !strings.Contains(err.Error(), "claude-api down") {
			t.Errorf("%s: got %v", name, err)
		}
	}
	plain := errors.New("bad request")
	if err := bothFailed(rt, unavailable(EndpointClaudeTeam, now.Add(time.Hour)), plain); !errors.Is(err, plain) || errors.Is(err, ErrUnavailable) {
		t.Errorf("a plain error of the fallback is returned as is, got %v", err)
	}
}

// countingClaude writes a fake CLI that prints stdout, exits with code and
// adds 1 line to the file count on each run.
func countingClaude(t *testing.T, stdout string, code int) (bin, count string) {
	t.Helper()
	dir := t.TempDir()
	count = filepath.Join(dir, "count")
	setCLIOutput(t, count, stdout, code)
	script := fmt.Sprintf("#!/bin/sh\nd=%q\ncat > /dev/null\necho run >> \"$d/count\"\ncat \"$d/stdout\"\nexit \"$(cat \"$d/code\")\"\n", dir)
	bin = filepath.Join(dir, "claude")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, count
}

// cliRuns returns how many times the fake CLI of countingClaude ran.
func cliRuns(t *testing.T, count string) int {
	t.Helper()
	b, err := os.ReadFile(count)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(b), "run")
}

// seatLimitedNoReset is a seat limit without a reset text: RetryAt is
// claudeRetryDelay later.
const seatLimitedNoReset = `{"type":"result","subtype":"success","is_error":true,"result":"Claude usage limit reached","usage":{}}`

// expireSkip ends the skip window of the seat in r, as if RetryAt passed.
// expireSkip ends the skip of the Team endpoint of r, and the down time of
// its seats: the window of the seat is over.
func expireSkip(r *Routing) {
	r.skips.mu.Lock()
	r.skips.m[EndpointClaudeTeam].until = time.Now().Add(-time.Second)
	r.skips.mu.Unlock()
	e := r.Endpoints[EndpointClaudeTeam].(*ClaudeTeamEndpoint)
	e.mu.Lock()
	for i := range e.seats {
		if !e.seats[i].down.IsZero() {
			e.seats[i].down = time.Now().Add(-time.Second)
		}
	}
	e.mu.Unlock()
}

func TestChatSkipsTheSeatUntilRetryAt(t *testing.T) {
	var logs bytes.Buffer
	prev := log.Logger
	log.Logger = zerolog.New(&logs)
	t.Cleanup(func() { log.Logger = prev })

	c, _ := chatTestClient(t, 200, `{}`)
	bin, count := countingClaude(t, seatLimitedNoReset, 1)
	f := newFakeMessagesAPI(t)
	r := fallbackRouting(t, bin, f)
	setRouting(t, r)
	call := func(task Task) {
		t.Helper()
		req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: task}
		if _, _, err := c.Chat(context.Background(), req); err != nil {
			t.Fatalf("%s: %v", task, err)
		}
	}
	call(TaskDetect)
	call(TaskDetect)
	call(TaskAudit) // the skip is per endpoint, not per task
	if n := cliRuns(t, count); n != 1 {
		t.Fatalf("the seat ran %d times, want 1: the calls in the window must skip it", n)
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("%d API calls, want 3", n)
	}

	expireSkip(r)
	call(TaskDetect) // tries the seat, which fails again: a new window
	call(TaskDetect)
	if n := cliRuns(t, count); n != 2 {
		t.Fatalf("the seat ran %d times, want 2: 1 call tries it after RetryAt", n)
	}

	setCLIOutput(t, count, teamSuccess, 0)
	expireSkip(r)
	call(TaskDetect) // the seat serves again
	call(TaskDetect)
	if n, api := cliRuns(t, count), len(f.requests()); n != 4 || api != 5 {
		t.Fatalf("seat runs %d (want 4), API calls %d (want 5): the seat must serve again", n, api)
	}
	if n := strings.Count(logs.String(), "skip it until it can serve again"); n != 1 {
		t.Fatalf("%d skip-start logs, want 1:\n%s", n, logs.String())
	}
	if n := strings.Count(logs.String(), "serves again"); n != 1 {
		t.Fatalf("%d serve-again logs, want 1:\n%s", n, logs.String())
	}
}

// setCLIOutput sets what the fake CLI of countingClaude prints and its exit
// code.
func setCLIOutput(t *testing.T, count, stdout string, code int) {
	t.Helper()
	dir := filepath.Dir(count)
	if err := os.WriteFile(filepath.Join(dir, "stdout"), []byte(stdout), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "code"), []byte(fmt.Sprint(code)), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestChatZeroRetryAtSkipsForAFixedTime(t *testing.T) {
	c, _ := chatTestClient(t, 200, `{}`)
	// A reset text that does not parse gives a zero RetryAt.
	bin, _ := countingClaude(t, `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit · resets Monday","usage":{}}`, 1)
	f := newFakeMessagesAPI(t)
	r := fallbackRouting(t, bin, f)
	setRouting(t, r)
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskDetect}
	start := time.Now()
	if _, _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	r.skips.mu.Lock()
	until := r.skips.m[EndpointClaudeTeam].until
	r.skips.mu.Unlock()
	if until.Before(start.Add(unknownResetSkip)) || until.After(time.Now().Add(unknownResetSkip)) {
		t.Fatalf("skip until %v, want about now + %v", until, unknownResetSkip)
	}
}

// A route without a fallback always calls the endpoint: the routing keeps no
// skip. The endpoint itself keeps its seat down until the reset, so the
// second call fails at once without a process; after the window, the call
// runs the seat again.
func TestChatWithoutFallbackDoesNotSkip(t *testing.T) {
	c, _ := chatTestClient(t, 200, `{}`)
	bin, count := countingClaude(t, seatLimitedNoReset, 1)
	r := ClaudeRouting(EndpointClaudeTeam, teamEndpoint(bin), "claude-sonnet-5-5")
	setRouting(t, r)
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskDetect}
	for i := 0; i < 3; i++ {
		if i == 2 {
			e := r.Endpoints[EndpointClaudeTeam].(*ClaudeTeamEndpoint)
			e.mu.Lock()
			e.seats[0].down = time.Now().Add(-time.Second)
			e.mu.Unlock()
		}
		_, _, err := c.Chat(context.Background(), req)
		var ue *UnavailableError
		if !errors.As(err, &ue) || ue.Endpoint != EndpointClaudeTeam || ue.RetryAt.IsZero() {
			t.Fatalf("call %d: want the seat limit with its retry time, got %v", i+1, err)
		}
	}
	if n := cliRuns(t, count); n != 2 {
		t.Fatalf("the seat ran %d times, want 2: the call in the window fails without a process, the call after it runs the seat", n)
	}
	if r.skips.m[EndpointClaudeTeam] != nil {
		t.Fatal("a route without a fallback must keep no skip")
	}
}

// Concurrent calls after the end of the window: exactly 1 call tries the
// seat, the others go to the API.
func TestChatSkipIsRaceFree(t *testing.T) {
	c, _ := chatTestClient(t, 200, `{}`)
	bin, count := countingClaude(t, seatLimitedNoReset, 1)
	f := newFakeMessagesAPI(t)
	r := fallbackRouting(t, bin, f)
	setRouting(t, r)
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskDetect}
	if _, _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	expireSkip(r)
	const n = 16
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := c.Chat(context.Background(), req); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if runs := cliRuns(t, count); runs != 2 {
		t.Fatalf("the seat ran %d times, want 2 (the first call and 1 try after the window)", runs)
	}
	if api := len(f.requests()); api != n+1 {
		t.Fatalf("%d API calls, want %d", api, n+1)
	}
}

// A cancelled probe does not go to the fallback and frees the probe slot: the
// next call tries the seat.
func TestChatCancelledProbeReleasesTheSlot(t *testing.T) {
	c, _ := chatTestClient(t, 200, `{}`)
	bin, count := countingClaude(t, seatLimitedNoReset, 1)
	f := newFakeMessagesAPI(t)
	r := fallbackRouting(t, bin, f)
	setRouting(t, r)
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskDetect}
	if _, _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	expireSkip(r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Chat(ctx, req); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the cancel", err)
	}
	if api := len(f.requests()); api != 1 {
		t.Fatalf("%d API calls, want 1: a cancelled call must not go to the fallback", api)
	}
	if _, _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if n := cliRuns(t, count); n != 2 {
		t.Fatalf("the seat ran %d times, want 2: the call after the cancel must try it", n)
	}
}

// Only the probe of a skip changes it: a call that started before the skip
// cannot end it.
func TestSkipIgnoresALateCall(t *testing.T) {
	var s endpointSkips
	limit := &UnavailableError{Endpoint: EndpointClaudeTeam, RetryAt: time.Now().Add(time.Hour), Err: ErrUnavailable}
	ok, early, _ := s.try(EndpointClaudeTeam) // starts before the skip
	if !ok || early.probe {
		t.Fatal("no skip: the call goes to the endpoint, not as a probe")
	}
	_, other, _ := s.try(EndpointClaudeTeam)
	s.record(context.Background(), EndpointClaudeTeam, other, limit) // starts the skip
	s.record(context.Background(), EndpointClaudeTeam, early, nil)   // a late success
	if ok, _, cause := s.try(EndpointClaudeTeam); ok || cause != limit {
		t.Fatalf("a late call ended the skip (ok=%v cause=%v)", ok, cause)
	}
	s.m[EndpointClaudeTeam].until = time.Now().Add(-time.Second)
	ok, probe, _ := s.try(EndpointClaudeTeam)
	if !ok || !probe.probe {
		t.Fatal("after RetryAt, 1 call probes the endpoint")
	}
	if ok, _, _ := s.try(EndpointClaudeTeam); ok {
		t.Fatal("a second call must not probe at the same time")
	}
	s.record(context.Background(), EndpointClaudeTeam, probe, nil)
	s.release(EndpointClaudeTeam, probe)
	if ok, tok, _ := s.try(EndpointClaudeTeam); !ok || tok.probe {
		t.Fatal("the success of the probe ends the skip")
	}
}

// TestClaudeAPIEndpoint_CapResetsWithTheDay: the worker lives for days; a cap
// reached today stops the endpoint until the next UTC day, then the spend
// starts again at 0.
func TestClaudeAPIEndpoint_CapResetsWithTheDay(t *testing.T) {
	f := newFakeMessagesAPI(t)
	e := f.endpoint()
	clock := time.Date(2026, 10, 18, 23, 50, 0, 0, time.UTC)
	e.now = func() time.Time { return clock }
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskAudit}
	e.maxUSD = apiReplyCost + apiReserve(t, req)
	for i := 0; i < 2; i++ {
		if _, _, err := e.Call(context.Background(), req, apiOpts); err != nil {
			t.Fatalf("call %d is under the cap: %v", i+1, err)
		}
	}
	_, _, err := e.Call(context.Background(), req, apiOpts)
	wantStopped(t, err, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC))

	clock = clock.Add(20 * time.Minute) // 00:10 the next day
	if _, _, err := e.Call(context.Background(), req, apiOpts); err != nil {
		t.Fatalf("the next day serves again: %v", err)
	}
	if math.Abs(e.spentUSD-apiReplyCost) > 1e-12 {
		t.Fatalf("spent %.6f USD, want the cost of the 1 reply of the new day", e.spentUSD)
	}
	if n := len(f.requests()); n != 3 {
		t.Fatalf("%d requests, want 3", n)
	}
}
