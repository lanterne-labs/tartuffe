package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

// resetBreaker returns the breaker to its cleared state between tests.
func resetBreaker() {
	primaryBreaker.mu.Lock()
	primaryBreaker.downUntil = time.Time{}
	primaryBreaker.mu.Unlock()
}

// TestDoRequest_BreakerSkipsPrimary: with the breaker armed and a fallback
// configured, DoRequest must not touch the primary at all and must serve the
// call from the fallback.
func TestDoRequest_BreakerSkipsPrimary(t *testing.T) {
	defer resetBreaker()

	var primaryHits, fallbackHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer fallback.Close()

	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_CHAT_MODEL", "test-fallback-model")
	t.Setenv("FALLBACK_KEY", "k")
	c := NewBaseLLMClient("breaker-test")

	MarkPrimaryDown("test")
	body, err := c.DoRequest(context.Background(), primary.URL+"/v1/chat/completions", "k", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("DoRequest with open breaker should succeed via fallback: %v", err)
	}
	if len(body) == 0 {
		t.Fatal("expected fallback body")
	}
	if primaryHits.Load() != 0 {
		t.Errorf("primary was hit %d times despite open breaker", primaryHits.Load())
	}
	if fallbackHits.Load() != 1 {
		t.Errorf("fallback hits = %d, want 1", fallbackHits.Load())
	}
}

// TestDoRequest_PrimarySuccessClearsBreaker: a cleared breaker routes to the
// primary, and success keeps/clears the breaker state.
func TestDoRequest_PrimarySuccessClearsBreaker(t *testing.T) {
	defer resetBreaker()

	var primaryHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryHits.Add(1)
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer primary.Close()

	t.Setenv("FALLBACK_URL", "")
	c := NewBaseLLMClient("breaker-test-2")

	resetBreaker()
	if _, err := c.DoRequest(context.Background(), primary.URL+"/v1/chat/completions", "k", []byte(`{"model":"m"}`)); err != nil {
		t.Fatalf("primary-only request failed: %v", err)
	}
	if primaryHits.Load() != 1 {
		t.Errorf("primary hits = %d, want 1", primaryHits.Load())
	}
	if PrimaryDown() {
		t.Error("breaker should be clear after primary success")
	}
}

// TestProbePrimary_NoFallbackConfigured: without a fallback there is nothing to
// flip to, so the probe must be a no-op that reports healthy.
func TestProbePrimary_NoFallbackConfigured(t *testing.T) {
	defer resetBreaker()
	t.Setenv("FALLBACK_URL", "")
	os.Unsetenv("FALLBACK_URL")
	if !ProbePrimary(context.Background()) {
		t.Error("probe without fallback configured must return true")
	}
}

// TestProbePrimary_DownArmsBreaker: a dead primary with a fallback configured
// must arm the breaker.
func TestProbePrimary_DownArmsBreaker(t *testing.T) {
	defer resetBreaker()
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	deadURL := dead.URL
	dead.Close() // now connection-refused

	t.Setenv("FALLBACK_URL", "http://127.0.0.1:1/v1")
	t.Setenv("OPEN_AI_API", deadURL)
	if ProbePrimary(context.Background()) {
		t.Fatal("probe against a closed port should fail")
	}
	if !PrimaryDown() {
		t.Error("failed probe must arm the breaker")
	}
}

// TestDoRequest_ClientErrorDoesNotArmBreaker: a 4xx from the primary is that
// one request's problem (here: a prompt over the model's context window). It
// must fail over for this call but leave the breaker CLOSED, so the next call —
// with a prompt that fits — still goes to the primary. Arming here is what
// pushed a whole pipeline run onto a credit-less fallback in prod.
func TestDoRequest_ClientErrorDoesNotArmBreaker(t *testing.T) {
	defer resetBreaker()

	var primaryHits, fallbackHits atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Reject only the first call, the way an oversized prompt would.
		if primaryHits.Add(1) == 1 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":{"message":"This model's maximum context length is 12000 tokens."}}`))
			return
		}
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fallbackHits.Add(1)
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer fallback.Close()

	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_CHAT_MODEL", "test-fallback-model")
	t.Setenv("FALLBACK_KEY", "k")
	c := NewBaseLLMClient("breaker-test-4xx")

	resetBreaker()
	if _, err := c.DoRequest(context.Background(), primary.URL+"/v1/chat/completions", "k", []byte(`{"model":"m"}`)); err != nil {
		t.Fatalf("oversized-prompt call should be served by the fallback: %v", err)
	}
	if fallbackHits.Load() != 1 {
		t.Errorf("fallback hits = %d, want 1 (the 4xx call must still fail over)", fallbackHits.Load())
	}
	if PrimaryDown() {
		t.Fatal("a 4xx must not arm the breaker — the endpoint is healthy")
	}

	// The next call must go to the primary, not the fallback.
	if _, err := c.DoRequest(context.Background(), primary.URL+"/v1/chat/completions", "k", []byte(`{"model":"m"}`)); err != nil {
		t.Fatalf("follow-up request failed: %v", err)
	}
	if primaryHits.Load() != 2 {
		t.Errorf("primary hits = %d, want 2 (breaker must not have diverted the second call)", primaryHits.Load())
	}
	if fallbackHits.Load() != 1 {
		t.Errorf("fallback hits = %d, want 1 (second call must not touch the fallback)", fallbackHits.Load())
	}
}

// TestIsEndpointOutage pins the breaker's arming policy. Driving a 5xx through
// DoRequest would burn the full two-minute retry backoff, so the classifier is
// tested directly — it is the only thing that decides whether an error speaks
// for the endpoint or just for one request.
func TestIsEndpointOutage(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"context overflow 400", &ClientError{StatusCode: 400, Status: "400 Bad Request", Body: "maximum context length is 12000 tokens"}, false},
		{"insufficient credits 402", &ClientError{StatusCode: 402, Status: "402 Payment Required", Body: "Insufficient credits"}, false},
		{"bad key 401", &ClientError{StatusCode: 401, Status: "401 Unauthorized", Body: ""}, false},
		{"wrapped client error", fmt.Errorf("LLM API request failed after retries: %w", &ClientError{StatusCode: 400, Status: "400 Bad Request", Body: "nope"}), false},
		{"server error", errors.New("LLM API server error: 500 Internal Server Error ()"), true},
		{"transport failure", errors.New("dial tcp: connect: connection refused"), true},
		{"sustained rate limit", errors.New("rate limit exceeded (status 429)"), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsEndpointOutage(tc.err); got != tc.want {
				t.Errorf("IsEndpointOutage(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestProbePrimary_WarmsColdModel: /models answering does not mean the model is
// loaded — llama-swap loads on the first completion and Cloudflare 524s past
// 100s, which used to arm the breaker at the start of every cold-model run.
// The probe must ride out warm-up 5xx and succeed once the model answers.
func TestProbePrimary_WarmsColdModel(t *testing.T) {
	defer resetBreaker()
	defer func(b, d time.Duration) { warmupBudget, warmupRetryDelay = b, d }(warmupBudget, warmupRetryDelay)
	warmupBudget, warmupRetryDelay = 5*time.Second, 10*time.Millisecond

	var chatHits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		if chatHits.Add(1) <= 2 {
			w.WriteHeader(524) // Cloudflare's origin-timeout while the model loads
			return
		}
		w.Write([]byte(`{"choices":[]}`))
	}))
	defer srv.Close()

	t.Setenv("FALLBACK_URL", "https://example.invalid/fallback")
	t.Setenv("OPEN_AI_API", srv.URL)

	resetBreaker()
	if !ProbePrimary(context.Background()) {
		t.Fatal("probe should succeed once the model warms up")
	}
	if PrimaryDown() {
		t.Error("breaker must be clear after a successful warm-up")
	}
	if chatHits.Load() != 3 {
		t.Errorf("chat hits = %d, want 3 (two loading 524s + one success)", chatHits.Load())
	}
}

// TestProbePrimary_WarmupExhaustedArmsBreaker: a model that never comes up is a
// real outage — the probe must give up within the budget and arm the breaker.
func TestProbePrimary_WarmupExhaustedArmsBreaker(t *testing.T) {
	defer resetBreaker()
	defer func(b, d time.Duration) { warmupBudget, warmupRetryDelay = b, d }(warmupBudget, warmupRetryDelay)
	warmupBudget, warmupRetryDelay = 200*time.Millisecond, 10*time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Write([]byte(`{"data":[]}`))
			return
		}
		w.WriteHeader(524)
	}))
	defer srv.Close()

	t.Setenv("FALLBACK_URL", "https://example.invalid/fallback")
	t.Setenv("OPEN_AI_API", srv.URL)

	resetBreaker()
	if ProbePrimary(context.Background()) {
		t.Fatal("probe should fail when the model never warms up")
	}
	if !PrimaryDown() {
		t.Error("breaker must be armed after warm-up exhaustion")
	}
}

// TestDoRequest_UnavailableOnlyWhenNoEndpointServes: a call fails with
// ErrUnavailable when the primary is down (breaker open) and the fallback
// rejects it too, but not when the primary itself rejected the request (a
// 4xx says nothing about the endpoint).
func TestDoRequest_UnavailableOnlyWhenNoEndpointServes(t *testing.T) {
	defer resetBreaker()

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"bad request"}`))
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		w.Write([]byte(`{"error":{"message":"Insufficient credits"}}`))
	}))
	defer fallback.Close()

	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_CHAT_MODEL", "test-fallback-model")
	t.Setenv("FALLBACK_KEY", "k")
	c := NewBaseLLMClient("unavailable-test")

	MarkPrimaryDown("test")
	_, err := c.DoRequest(context.Background(), primary.URL+"/v1/chat/completions", "k", []byte(`{"model":"m"}`))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("breaker open + fallback 402: want ErrUnavailable, got %v", err)
	}

	resetBreaker()
	_, err = c.DoRequest(context.Background(), primary.URL+"/v1/chat/completions", "k", []byte(`{"model":"m"}`))
	if err == nil || errors.Is(err, ErrUnavailable) {
		t.Fatalf("primary 400 + fallback 402: want a plain error, got %v", err)
	}
}

// TestWaitForPrimary_ComesBack: the wait polls a primary that is down, returns
// once it answers, and clears the breaker so calls go to it at once. It
// probes without a fallback configured.
func TestWaitForPrimary_ComesBack(t *testing.T) {
	defer resetBreaker()
	defer func(d time.Duration) { waitPollInterval = d }(waitPollInterval)
	waitPollInterval = 10 * time.Millisecond

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/chat/completions" {
			w.Write([]byte(`{"choices":[]}`))
			return
		}
		if r.URL.Path != "/v1/models" {
			t.Errorf("probe path = %s, want /v1/models", r.URL.Path)
		}
		if hits.Add(1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("OPEN_AI_API", srv.URL+"/v1/")

	MarkPrimaryDown("test")
	back, polls := waitForPrimary(context.Background(), time.Now().Add(time.Minute))
	if !back || polls != 3 {
		t.Fatalf("back=%v polls=%d; want back after 3 polls (two down, one up)", back, polls)
	}
	if PrimaryDown() {
		t.Error("the breaker must be cleared once the primary is back")
	}
}

// TestWaitForPrimary_FirstProbeAfterInterval: even a healthy endpoint is
// probed only after one poll interval, so a request that keeps failing on it
// is not retried in a tight loop.
func TestWaitForPrimary_FirstProbeAfterInterval(t *testing.T) {
	defer resetBreaker()
	defer func(d time.Duration) { waitPollInterval = d }(waitPollInterval)
	waitPollInterval = 50 * time.Millisecond

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("OPEN_AI_API", srv.URL)

	start := time.Now()
	back, polls := waitForPrimary(context.Background(), time.Now().Add(time.Minute))
	if !back || polls != 1 {
		t.Fatalf("back=%v polls=%d; want back on the first poll", back, polls)
	}
	if elapsed := time.Since(start); elapsed < waitPollInterval {
		t.Errorf("first probe after %v, want at least one interval (%v)", elapsed, waitPollInterval)
	}
}

// TestWaitForPrimary_Deadline: a primary that never comes back ends the wait
// at the deadline with false; a cancelled context and a malformed URL end it
// too.
func TestWaitForPrimary_Deadline(t *testing.T) {
	defer resetBreaker()
	defer func(d time.Duration) { waitPollInterval = d }(waitPollInterval)
	waitPollInterval = 10 * time.Millisecond

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("OPEN_AI_API", srv.URL)

	start := time.Now()
	if back, _ := waitForPrimary(context.Background(), start.Add(100*time.Millisecond)); back {
		t.Fatal("a primary that never answers must not be reported back")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("wait lasted %v past a 100ms deadline", elapsed)
	}
	if hits.Load() < 2 {
		t.Errorf("probes = %d, want the endpoint polled until the deadline", hits.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if back, _ := waitForPrimary(ctx, time.Now().Add(time.Hour)); back {
		t.Fatal("a cancelled wait must not report the primary back")
	}

	t.Setenv("OPEN_AI_API", "http://[::1")
	if back, polls := waitForPrimary(context.Background(), time.Now().Add(time.Hour)); back || polls != 1 {
		t.Fatalf("back=%v polls=%d; a malformed URL must end the wait, not count as back", back, polls)
	}
}

// TestWaitForPrimary_ChatMustAnswer: /models answering is not enough; a chat
// model that keeps failing keeps the wait going until the deadline, with one
// warm-up completion per poll.
func TestWaitForPrimary_ChatMustAnswer(t *testing.T) {
	defer resetBreaker()
	defer func(d time.Duration) { waitPollInterval = d }(waitPollInterval)
	waitPollInterval = 10 * time.Millisecond

	var chats, models atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/chat/completions" {
			chats.Add(1)
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		models.Add(1)
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("OPEN_AI_API", srv.URL)

	if back, _ := waitForPrimary(context.Background(), time.Now().Add(150*time.Millisecond)); back {
		t.Fatal("a failing chat model must not be reported back")
	}
	if chats.Load() == 0 || chats.Load() != models.Load() {
		t.Errorf("chats=%d models=%d; want one warm-up completion per poll", chats.Load(), models.Load())
	}
}

// seatLimit is the error of a Claude seat at its usage limit until retryAt.
func seatLimit(retryAt time.Time) error {
	return &UnavailableError{
		Endpoint: EndpointClaudeTeam,
		RetryAt:  retryAt,
		Err:      fmt.Errorf("%w: claude seat %q reached its usage limit", ErrUnavailable, "alice"),
	}
}

// countingPrimary points OPEN_AI_API at a healthy server and counts its
// requests.
func countingPrimary(t *testing.T) *atomic.Int32 {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(srv.Close)
	t.Setenv("FALLBACK_URL", "")
	t.Setenv("OPEN_AI_API", srv.URL)
	return &hits
}

// TestWaitForEndpoint_SeatWaitsForRetryAt: a seat limit waits until its
// retry time and does not probe the local endpoint. A retry time after the
// end of the wait, or a retry time that is not known, gives false at once.
func TestWaitForEndpoint_SeatWaitsForRetryAt(t *testing.T) {
	defer func(d time.Duration) { waitPollInterval = d }(waitPollInterval)
	waitPollInterval = 10 * time.Millisecond
	hits := countingPrimary(t)

	retryAt := time.Now().Add(100 * time.Millisecond)
	cause := fmt.Errorf("phase: %w", seatLimit(retryAt))
	back, polls := WaitForEndpoint(context.Background(), cause, time.Now().Add(time.Minute))
	if !back || polls != 1 || time.Now().Before(retryAt) {
		t.Fatalf("back=%v polls=%d at %v; want back after 1 poll, not before %v", back, polls, time.Now(), retryAt)
	}

	start := time.Now()
	back, _ = WaitForEndpoint(context.Background(), seatLimit(time.Now().Add(3*time.Hour)), time.Now().Add(30*time.Minute))
	if back || time.Since(start) > time.Second {
		t.Fatalf("back=%v after %v; a reset after the end of the wait must give false at once", back, time.Since(start))
	}
	start = time.Now()
	back, _ = WaitForEndpoint(context.Background(), seatLimit(time.Time{}), time.Now().Add(30*time.Minute))
	if back || time.Since(start) > time.Second {
		t.Fatalf("back=%v after %v; a reset time that is not known must give false at once", back, time.Since(start))
	}
	if hits.Load() != 0 {
		t.Errorf("the local endpoint got %d probes for a seat limit; want 0", hits.Load())
	}
}

// TestWaitForEndpoint_LocalErrorProbes: an outage without an UnavailableError
// is a local outage: the wait probes the local endpoint.
func TestWaitForEndpoint_LocalErrorProbes(t *testing.T) {
	defer resetBreaker()
	defer func(d time.Duration) { waitPollInterval = d }(waitPollInterval)
	waitPollInterval = 10 * time.Millisecond
	hits := countingPrimary(t)

	cause := fmt.Errorf("%w: primary down", ErrUnavailable)
	back, polls := WaitForEndpoint(context.Background(), cause, time.Now().Add(time.Minute))
	if !back || polls != 1 || hits.Load() == 0 {
		t.Fatalf("back=%v polls=%d probes=%d; want the local endpoint probed and back", back, polls, hits.Load())
	}
}

// TestBreakerIgnoresClaude: a Claude failure does not open the breaker of the
// local endpoint, and an open breaker does not stop the Claude calls.
func TestBreakerIgnoresClaude(t *testing.T) {
	c, _ := chatTestClient(t, 200, `{"choices":[{"message":{"role":"assistant","content":"server"},"finish_reason":"stop"}]}`)
	t.Setenv("FALLBACK_URL", "http://127.0.0.1:1")
	t.Setenv("FALLBACK_CHAT_MODEL", "fb")
	req := ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "x"}}, MaxTokens: 10, Task: TaskDetect}

	limited, _ := fakeClaude(t, `{"type":"result","subtype":"success","is_error":true,"result":"You've hit your limit · resets 3pm (Europe/Paris)","usage":{}}`, "", 1)
	setRouting(t, ClaudeRouting(EndpointClaudeTeam, teamEndpoint(limited), "claude-sonnet-5-5"))
	_, _, err := c.Chat(context.Background(), req)
	var ue *UnavailableError
	if !errors.As(err, &ue) {
		t.Fatalf("err = %v, want a seat limit", err)
	}
	if PrimaryDown() {
		t.Fatal("a seat limit opened the breaker of the local endpoint")
	}

	MarkPrimaryDown("test")
	bin, _ := fakeClaude(t, teamSuccess, "", 0)
	setRouting(t, ClaudeRouting(EndpointClaudeTeam, teamEndpoint(bin), "claude-sonnet-5-5"))
	if _, _, err := c.Chat(context.Background(), req); err != nil {
		t.Fatalf("an open breaker stopped a Claude call: %v", err)
	}
	if !PrimaryDown() {
		t.Fatal("a Claude success cleared the breaker of the local endpoint")
	}
}
