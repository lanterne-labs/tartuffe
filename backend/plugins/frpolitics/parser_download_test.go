package frpolitics

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"
)

// TestDownloadAndParseScrutins downloads Scrutins.json.zip and parses scrutin JSON files from it.
// The file is cached so it won't download again if already present.
func TestDownloadAndParseScrutins(t *testing.T) {
	skipANTests(t)
	ctx := context.Background()
	legislature := 16 // Test with legislature 16
	url := fmt.Sprintf("https://data.assemblee-nationale.fr/static/openData/repository/%d/scrutins/Scrutins.json.zip", legislature)

	// Download (or use cached) the zip file
	body, err := downloadCached(ctx, url, "asset/cache", 0)
	if err != nil {
		t.Fatalf("failed to download scrutins zip: %v", err)
	}

	// Extract and parse JSON files from the zip
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("failed to open zip: %v", err)
	}

	parsedCount := 0
	failedCount := 0
	maxFiles := 50 // Test more files to catch parsing errors

	for i, f := range zr.File {
		if i >= maxFiles {
			break
		}
		if !strings.HasSuffix(strings.ToLower(f.Name), ".json") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			t.Logf("failed to open zip entry %s: %v", f.Name, err)
			continue
		}

		data, err := io.ReadAll(io.LimitReader(rc, 10<<20)) // 10MB limit per file
		rc.Close()
		if err != nil {
			t.Logf("failed to read zip entry %s: %v", f.Name, err)
			continue
		}

		// Try to parse as scrutinJSON
		var s scrutinJSON
		if err := json.Unmarshal(data, &s); err != nil {
			failedCount++
			t.Logf("failed to unmarshal scrutin from %s: %v", f.Name, err)
			// Log first 500 chars of JSON for debugging
			preview := string(data)
			if len(preview) > 500 {
				preview = preview[:500] + "..."
			}
			t.Logf("JSON preview: %s", preview)
			continue
		}

		// Validate parsed data
		if s.Scrutin.Legislature == 0 {
			t.Logf("warning: scrutin from %s has zero legislature", f.Name)
		}
		if int(s.Scrutin.Numero) == 0 {
			t.Logf("warning: scrutin from %s has zero numero", f.Name)
		}

		parsedCount++
	}

	if parsedCount == 0 {
		t.Fatalf("failed to parse any scrutin JSON files from zip")
	}

	t.Logf("successfully parsed %d scrutin JSON files", parsedCount)
	if failedCount > 0 {
		t.Logf("failed to parse %d scrutin JSON files", failedCount)
	}
}
