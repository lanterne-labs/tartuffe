package search

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A transport error quotes the whole URL, key included; redaction keeps the
// diagnosis and drops the secret.
func TestRedactSecrets(t *testing.T) {
	transportErr := &url.Error{
		Op:  "Get",
		URL: "https://serpapi.com/search.json?api_key=sk-live-SECRET123&engine=google_news&q=Emmanuel+Macron",
		Err: errors.New("dial tcp: lookup serpapi.com: no such host"),
	}
	got := redactSecrets(transportErr).Error()
	if strings.Contains(got, "SECRET123") {
		t.Fatalf("the key survived redaction: %s", got)
	}
	for _, want := range []string{"api_key=REDACTED", "engine=google_news", "q=Emmanuel+Macron", "no such host"} {
		if !strings.Contains(got, want) {
			t.Fatalf("redaction dropped %q from: %s", want, got)
		}
	}

	// Other secret-ish parameter names, in any position and any case.
	for _, raw := range []string{
		"Post \"https://x.test/v1?q=1&token=abc123\": refused",
		"Get \"https://x.test/v1?API_KEY=abc123\": refused",
		"Get \"https://x.test/v1?key=abc123&page=2\": refused",
	} {
		got := redactSecrets(errors.New(raw)).Error()
		if strings.Contains(got, "abc123") {
			t.Fatalf("secret survived in %q → %q", raw, got)
		}
	}

	if redactSecrets(nil) != nil {
		t.Fatal("nil must stay nil")
	}
	if got := redactSecrets(errors.New("plain failure")).Error(); got != "plain failure" {
		t.Fatalf("a secret-free error must be unchanged, got %q", got)
	}
}

// The redacted error must not carry the raw URL in its chain either: a
// caller unwrapping to *url.Error would print the key again.
func TestRedactSecretsDropsTheChain(t *testing.T) {
	transportErr := &url.Error{Op: "Get", URL: "https://serpapi.com/search.json?api_key=sk-live-SECRET123", Err: http.ErrHandlerTimeout}
	wrapped := fmt.Errorf("failed to execute search request: %w", redactSecrets(transportErr))
	var asURL *url.Error
	if errors.As(wrapped, &asURL) {
		t.Fatalf("the unredacted URL is still reachable: %s", asURL.URL)
	}
	if strings.Contains(fmt.Sprintf("%+v", wrapped), "SECRET123") {
		t.Fatal("the key is still in the formatted error")
	}
}
