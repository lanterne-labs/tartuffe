package frpolitics

import (
	"context"
	"strings"
	"testing"
)

// TestDownloadAndParseActors verifies we can load the AMO20 actors mapping (id -> human name)
// from the cached zip (preferred) or remote if not cached.
func TestDownloadAndParseActors(t *testing.T) {
	ctx := context.Background()
	// Only read local cache; do NOT download
	m, err := loadActorNameMapFromPath(ctx, "asset/cache/Organes.json.zip")
	if err != nil {
		t.Fatalf("failed to read local actors mapping zip: %v", err)
	}
	if len(m) == 0 {
		t.Fatalf("expected non-empty actor mapping from local cache, got 0")
	}
	// Find a few samples with 'PA' prefix and a non-empty human name
	count := 0
	for k, v := range m {
		if strings.HasPrefix(strings.ToUpper(k), "PA") && strings.TrimSpace(v) != "" {
			t.Logf("sample: %s -> %s", k, v)
			count++
			if count >= 5 {
				break
			}
		}
	}
	if count == 0 {
		t.Fatalf("expected at least one 'PA' actor entry with a human name; map size=%d", len(m))
	}
}
