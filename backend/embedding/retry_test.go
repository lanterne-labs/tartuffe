package embedding

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newTestGenerator builds a generator pointed at a test server, with no
// fallback so sendEmbedOnce is exercised in isolation.
func newTestGenerator(url string) *OpenAIEmbeddingGenerator {
	return &OpenAIEmbeddingGenerator{
		apiKey:     "k",
		apiURL:     url,
		model:      "test-embed",
		httpClient: &http.Client{Timeout: 10 * time.Second},
	}
}

// TestSendEmbedOnce_RetriesServerError: a transient 5xx must be retried rather
// than returned. The caller arms a process-wide breaker on a failed embeddings
// call, so surfacing a one-off 502 from a tunnel reconnect diverted every
// subsequent call — chat included — to the fallback for 10 minutes.
func TestSendEmbedOnce_RetriesServerError(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway) // the Cloudflare tunnel blip
			return
		}
		w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	_, status, err := newTestGenerator(srv.URL).sendEmbedOnce(
		context.Background(), srv.URL, "k", nil, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("transient 502 should have been retried, got error: %v", err)
	}
	if status != http.StatusOK {
		t.Errorf("status = %d, want 200 (the retry succeeded)", status)
	}
	if hits.Load() != 2 {
		t.Errorf("server hits = %d, want 2 (one failure + one retry)", hits.Load())
	}
}

// TestSendEmbedOnce_SustainedServerErrorSurfaces: once retries are exhausted the
// 5xx IS returned, so a genuinely dead endpoint still arms the breaker.
func TestSendEmbedOnce_SustainedServerErrorSurfaces(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	_, status, err := newTestGenerator(srv.URL).sendEmbedOnce(
		context.Background(), srv.URL, "k", nil, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if status != http.StatusBadGateway {
		t.Errorf("status = %d, want 502 surfaced after retries", status)
	}
	if hits.Load() != maxRetries+1 {
		t.Errorf("server hits = %d, want %d (initial + %d retries)", hits.Load(), maxRetries+1, maxRetries)
	}
}

// TestSendEmbedOnce_ClientErrorNotRetried: a 4xx is this request's problem, so
// it returns immediately — retrying an oversized or malformed payload is waste.
func TestSendEmbedOnce_ClientErrorNotRetried(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	_, status, err := newTestGenerator(srv.URL).sendEmbedOnce(
		context.Background(), srv.URL, "k", nil, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", status)
	}
	if hits.Load() != 1 {
		t.Errorf("server hits = %d, want 1 (4xx must not be retried)", hits.Load())
	}
}
