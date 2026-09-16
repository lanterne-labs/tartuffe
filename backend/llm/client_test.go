package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestPayloadWithModel(t *testing.T) {
	out, err := payloadWithModel([]byte(`{"model":"primary","messages":[]}`), "fb-model")
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	if m["model"] != "fb-model" {
		t.Fatalf("model not swapped, got %v", m["model"])
	}
	if _, ok := m["messages"]; !ok {
		t.Fatal("other fields (messages) must be preserved")
	}
	// Empty model leaves the payload untouched.
	same, err := payloadWithModel([]byte(`{"model":"x"}`), "")
	if err != nil || string(same) != `{"model":"x"}` {
		t.Fatalf("empty model should be a no-op, got %s (%v)", same, err)
	}
}

// TestDoRequest_FailsOverToFallback drives the primary→fallback path with two
// httptest servers: the primary returns 401 (a permanent error, so failover is
// immediate — no 2-minute backoff), and the fallback serves the completion.
// It asserts CF-Access headers hit the primary only and the model is swapped.
func TestDoRequest_FailsOverToFallback(t *testing.T) {
	defer resetBreaker()
	resetBreaker()
	var primaryCFID string
	var fbModel, fbAuth, fbCF string
	var fbHit bool

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCFID = r.Header.Get("CF-Access-Client-Id")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer primary.Close()

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fbHit = true
		fbAuth = r.Header.Get("Authorization")
		fbCF = r.Header.Get("CF-Access-Client-Id")
		body, _ := io.ReadAll(r.Body)
		var m map[string]any
		_ = json.Unmarshal(body, &m)
		fbModel, _ = m["model"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer fallback.Close()

	t.Setenv("CF_ACCESS_CLIENT_ID", "cf-id")
	t.Setenv("CF_ACCESS_CLIENT_SECRET", "cf-secret")
	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_KEY", "fb-key")
	t.Setenv("FALLBACK_CHAT_MODEL", "fb-model")

	c := NewBaseLLMClient("test")
	c.httpClient = &http.Client{Timeout: 2 * time.Second}

	payload := []byte(`{"model":"primary-model","messages":[{"role":"user","content":"hi"}]}`)
	body, err := c.DoRequest(context.Background(), primary.URL+"/chat/completions", "primary-key", payload)
	if err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if !fbHit {
		t.Fatal("fallback endpoint was never tried")
	}
	if primaryCFID != "cf-id" {
		t.Fatalf("primary should receive CF-Access header, got %q", primaryCFID)
	}
	if fbCF != "" {
		t.Fatalf("fallback must NOT receive CF-Access header, got %q", fbCF)
	}
	if fbModel != "fb-model" {
		t.Fatalf("fallback model not swapped, got %q", fbModel)
	}
	if fbAuth != "Bearer fb-key" {
		t.Fatalf("fallback auth wrong, got %q", fbAuth)
	}
	if len(body) == 0 {
		t.Fatal("expected a response body from the fallback")
	}
}

// TestDoRequest_PrimarySucceeds_NoFallback confirms the fallback is not touched
// when the primary works (and works with no fallback configured).
func TestDoRequest_PrimarySucceeds_NoFallback(t *testing.T) {
	defer resetBreaker()
	resetBreaker()
	var fbHit bool
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fbHit = true }))
	defer fallback.Close()

	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_KEY", "fb-key")
	t.Setenv("FALLBACK_CHAT_MODEL", "fb-model")

	c := NewBaseLLMClient("test")
	c.httpClient = &http.Client{Timeout: 2 * time.Second}
	if _, err := c.DoRequest(context.Background(), primary.URL+"/chat/completions", "k", []byte(`{"model":"m","messages":[]}`)); err != nil {
		t.Fatalf("DoRequest: %v", err)
	}
	if fbHit {
		t.Fatal("fallback should not be tried when primary succeeds")
	}
}

func TestIsCreditError(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   bool
	}{
		{402, "payment required", true},
		{402, "", true},
		{200, "insufficient credits", true},
		{200, "Insufficient_Quota reached", true},
		{429, "RESOURCE_EXHAUSTED: quota exceeded", true},
		{429, "rate limit exceeded", false},
		{500, "internal server error", false},
		{200, "ok", false},
	}
	for _, tt := range tests {
		got := isCreditError(tt.status, tt.body)
		if got != tt.want {
			t.Errorf("isCreditError(%d, %q) = %v, want %v", tt.status, tt.body, got, tt.want)
		}
	}
}
