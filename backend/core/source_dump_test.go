package core

import (
	"crypto/sha256"
	"digital-mirror/backend"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDumpSourceAndFacts(t *testing.T) {
	tmpDir := t.TempDir()

	sourceURL := "https://example.com/article/test-123"
	title := "Test Article Title"
	content := "Full article text content sent to LLM for extraction."
	published := time.Date(2026, 2, 11, 10, 0, 0, 0, time.UTC)

	facts := []backend.Fact{
		{
			SubjectEntity:      "Marine Le Pen",
			NormalizedFactText: "Marine Le Pen voted against the budget bill",
			OriginalQuote:      "Le Pen cast her vote against the proposed budget bill.",
			CanonicalVerb:      "VOTED_AGAINST",
		},
		{
			SubjectEntity:      "Marine Le Pen",
			NormalizedFactText: "Marine Le Pen called for new elections",
			OriginalQuote:      "She demanded new elections be held immediately.",
			CanonicalVerb:      "DEMANDED",
		},
	}

	err := DumpSourceAndFacts(tmpDir, "Marine Le Pen", sourceURL, title, content, &published, facts)
	if err != nil {
		t.Fatalf("DumpSourceAndFacts failed: %v", err)
	}

	// Verify file exists at expected path
	hash := sha256.Sum256([]byte(sourceURL))
	expectedFilename := hex.EncodeToString(hash[:]) + ".json"
	expectedPath := filepath.Join(tmpDir, "marine_le_pen", expectedFilename)

	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Expected file not found at %s: %v", expectedPath, err)
	}

	// Parse and verify content
	var dump sourceDump
	if err := json.Unmarshal(data, &dump); err != nil {
		t.Fatalf("Failed to parse dumped JSON: %v", err)
	}

	if dump.SourceURL != sourceURL {
		t.Errorf("SourceURL = %q, want %q", dump.SourceURL, sourceURL)
	}
	if dump.Title != title {
		t.Errorf("Title = %q, want %q", dump.Title, title)
	}
	if dump.Content != content {
		t.Errorf("Content = %q, want %q", dump.Content, content)
	}
	if dump.DatePublished == nil || !dump.DatePublished.Equal(published) {
		t.Errorf("DatePublished = %v, want %v", dump.DatePublished, published)
	}
	if len(dump.ExtractedFacts) != 2 {
		t.Fatalf("ExtractedFacts length = %d, want 2", len(dump.ExtractedFacts))
	}
	if dump.ExtractedFacts[0].SubjectEntity != "Marine Le Pen" {
		t.Errorf("ExtractedFacts[0].SubjectEntity = %q, want %q", dump.ExtractedFacts[0].SubjectEntity, "Marine Le Pen")
	}
	if dump.ExtractedFacts[0].NormalizedFact != "Marine Le Pen voted against the budget bill" {
		t.Errorf("ExtractedFacts[0].NormalizedFact = %q", dump.ExtractedFacts[0].NormalizedFact)
	}
	if dump.ExtractedFacts[1].OriginalQuote != "She demanded new elections be held immediately." {
		t.Errorf("ExtractedFacts[1].OriginalQuote = %q", dump.ExtractedFacts[1].OriginalQuote)
	}
}

func TestDumpSourceAndFacts_EmptyFacts(t *testing.T) {
	tmpDir := t.TempDir()

	sourceURL := "https://example.com/empty-article"
	err := DumpSourceAndFacts(tmpDir, "Test Person", sourceURL, "Empty Article", "Some content", nil, nil)
	if err != nil {
		t.Fatalf("DumpSourceAndFacts with empty facts failed: %v", err)
	}

	hash := sha256.Sum256([]byte(sourceURL))
	expectedFilename := hex.EncodeToString(hash[:]) + ".json"
	expectedPath := filepath.Join(tmpDir, "test_person", expectedFilename)

	data, err := os.ReadFile(expectedPath)
	if err != nil {
		t.Fatalf("Expected file not found: %v", err)
	}

	var dump sourceDump
	if err := json.Unmarshal(data, &dump); err != nil {
		t.Fatalf("Failed to parse JSON: %v", err)
	}

	if len(dump.ExtractedFacts) != 0 {
		t.Errorf("ExtractedFacts length = %d, want 0", len(dump.ExtractedFacts))
	}
	if dump.DatePublished != nil {
		t.Errorf("DatePublished = %v, want nil", dump.DatePublished)
	}
}
