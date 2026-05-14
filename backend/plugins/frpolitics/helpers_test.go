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

// skipANTests skips tests that hit data.assemblee-nationale.fr. That endpoint
// rate-limits aggressively (HTTP 429) on both residential IPs and CI runners,
// so by default these tests are skipped everywhere. Set RUN_AN_TESTS=1 to
// run them manually when you know the endpoint is responsive.
func skipANTests(t *testing.T) {
	t.Helper()
	if os.Getenv("RUN_AN_TESTS") != "1" {
		t.Skip("skipping AN-endpoint integration test (set RUN_AN_TESTS=1 to run; endpoint rate-limits both local IPs and CI)")
	}
}
