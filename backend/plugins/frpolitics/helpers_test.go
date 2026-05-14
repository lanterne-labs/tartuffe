package frpolitics

import (
	"os"
	"testing"
)

// skipIfCI skips the calling test when running under GitHub Actions (or any
// other runner that sets CI=true). Used by tests that take a long time or
// require local-only resources (Supabase, OPEN_AI_KEY, etc.).
func skipIfCI(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Skip("skipping local-only integration test in CI")
	}
}

// skipIfNotCI skips the calling test when NOT running under CI. Used by tests
// that hit data.assemblee-nationale.fr — the endpoint rate-limits residential
// IPs (HTTP 429) but works fine from CI runners.
func skipIfNotCI(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") == "" {
		t.Skip("skipping CI-only integration test (would hit assemblee-nationale.fr and be rate-limited locally)")
	}
}
