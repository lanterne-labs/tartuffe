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
			if got := isEndpointOutage(tc.err); got != tc.want {
				t.Errorf("isEndpointOutage(%v) = %v, want %v", tc.err, got, tc.want)
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
