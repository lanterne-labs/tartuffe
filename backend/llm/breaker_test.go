package llm

import (
	"context"
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
