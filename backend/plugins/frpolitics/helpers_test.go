package frpolitics

import (
	"os"
	"testing"
)

// skipIfCI skips the calling test when running under GitHub Actions (or any
// other runner that sets CI=true). Used by tests that hit
// data.assemblee-nationale.fr — the endpoint rate-limits CI runners
// (HTTP 429) and there is no sandboxed mirror to point at.
func skipIfCI(t *testing.T) {
	t.Helper()
	if os.Getenv("CI") != "" {
		t.Skip("skipping integration test in CI: hits external assemblee-nationale.fr endpoints (HTTP 429)")
	}
}
