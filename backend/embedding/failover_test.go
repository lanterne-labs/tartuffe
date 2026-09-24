package embedding

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"digital-mirror/backend/llm"
)

// TestGenerateEmbeddings_FailsOverToFallback drives the embedding primary→fallback
// path hermetically: the primary returns 503, the fallback serves a valid
// embedding. It also asserts encoding_format is pinned to "float" and CF-Access
// headers hit the primary only.
func TestGenerateEmbeddings_FailsOverToFallback(t *testing.T) {
	var fbHit bool
	var fbFormat, fbCF, primaryCF string

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		primaryCF = r.Header.Get("CF-Access-Client-Id")
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer primary.Close()

	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fbHit = true
		fbCF = r.Header.Get("CF-Access-Client-Id")
		body, _ := io.ReadAll(r.Body)
		var req map[string]any
		_ = json.Unmarshal(body, &req)
		fbFormat, _ = req["encoding_format"].(string)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2,0.3],"index":0}]}`)
	}))
	defer fallback.Close()

	t.Setenv("OPEN_AI_KEY", "primary-key")
	t.Setenv("OPEN_AI_API", primary.URL)
	t.Setenv("OPEN_AI_EMBED_MODEL", "test-embed")
	t.Setenv("CF_ACCESS_CLIENT_ID", "cf-id")
	t.Setenv("CF_ACCESS_CLIENT_SECRET", "cf-secret")
	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_KEY", "fb-key")

	g, err := NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	embs, err := g.GenerateEmbeddings(context.Background(), PurposeFactStorage, []string{"hello"})
	if err != nil {
		t.Fatalf("GenerateEmbeddings: %v", err)
	}
	if !fbHit {
		t.Fatal("fallback endpoint was never tried")
	}
	if primaryCF != "cf-id" {
		t.Fatalf("primary should receive CF-Access header, got %q", primaryCF)
	}
	if fbCF != "" {
		t.Fatalf("fallback must NOT receive CF-Access header, got %q", fbCF)
	}
	if fbFormat != "float" {
		t.Fatalf("encoding_format not pinned to float, got %q", fbFormat)
	}
	if len(embs) != 1 || len(embs[0]) != 3 {
		t.Fatalf("unexpected embeddings shape: %v", embs)
	}
}

// TestGenerateEmbeddings_UnavailableOnlyWhenNoEndpointServes mirrors the chat
// client's rule (llm TestDoRequest_UnavailableOnlyWhenNoEndpointServes): an
// embeddings call fails with llm.ErrUnavailable when the primary is down
// (breaker open) and the fallback rejects it too, but not when the primary
// itself rejected the request (a 4xx says nothing about the endpoint).
func TestGenerateEmbeddings_UnavailableOnlyWhenNoEndpointServes(t *testing.T) {
	defer llm.MarkPrimaryUp()

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request"}}`)
	}))
	defer primary.Close()
	fallback := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPaymentRequired)
		_, _ = io.WriteString(w, `{"error":{"message":"Insufficient credits"}}`)
	}))
	defer fallback.Close()

	t.Setenv("OPEN_AI_KEY", "primary-key")
	t.Setenv("OPEN_AI_API", primary.URL)
	t.Setenv("OPEN_AI_EMBED_MODEL", "test-embed")
	t.Setenv("FALLBACK_URL", fallback.URL)
	t.Setenv("FALLBACK_KEY", "fb-key")
	g, err := NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		t.Fatal(err)
	}

	llm.MarkPrimaryDown("test")
	_, err = g.GenerateEmbeddings(context.Background(), PurposeFactStorage, []string{"hello"})
	if !errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("breaker open + fallback 402: want ErrUnavailable, got %v", err)
	}

	llm.MarkPrimaryUp()
	_, err = g.GenerateEmbeddings(context.Background(), PurposeFactStorage, []string{"hello"})
	if err == nil || errors.Is(err, llm.ErrUnavailable) {
		t.Fatalf("primary 400 + fallback 402: want a plain error, got %v", err)
	}
	if llm.PrimaryDown() {
		t.Fatal("a primary 400 must not arm the breaker")
	}
}
