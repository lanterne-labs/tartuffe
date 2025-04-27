package categorizer

import (
	// "digital-mirror/backend/extractor" // Remove this unused import
	"digital-mirror/backend/extractor"
	"os"
	"testing"
	time "time"

	"github.com/joho/godotenv"
)

// Helper function to load .env for tests
// Consider moving this to a common test helper package if used in multiple places.
func loadEnv(t *testing.T) {
	t.Helper()
	// Assumes .env is at the project root
	err := godotenv.Load("../../.env")
	if err != nil {
		t.Log("INFO: .env file not found or failed to load, tests needing env vars might be skipped.")
	}
}

// TestRemoteLLMExtractor_ExtractFacts_Mistral tests the fact extraction using the Mistral configuration.
// It requires MISTRAL_API_KEY environment variable to be set (e.g., in .env).
func TestRemoteLLMExtractor_ExtractFacts_Mistral(t *testing.T) {
	loadEnv(t)

	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping Mistral Fact Extraction test: MISTRAL_API_KEY not set.")
	}

	// Create the extractor configured for Mistral
	mistralExtractor, err := NewMistralExtractorFromEnv()
	if err != nil {
		t.Fatalf("Failed to create MistralExtractor: %v", err)
	}

	// Create mock content
	// Use a simple text containing potential facts about multiple people.
	mockURL := "http://example.com/test-article"
	mockText := `
	At the recent summit, Alice presented a new proposal regarding environmental regulations.
	Bob later commented that the proposal was ambitious but necessary.
	Separately, Charlie mentioned ongoing efforts to improve local infrastructure.
	Alice also thanked Bob for his input during the conference call.
	David has not commented yet.
	`
	mockContent := extractor.ExtractedContent{
		URL:           mockURL,
		Title:         "Test Summit Report",
		Content:       mockText,
		DatePublished: nil, // Or set a mock time if needed
	}

	// Target person for the prompt context
	personName := "Alice"
	t.Logf("Attempting to extract facts related to '%s' using Mistral...", personName)

	startTime := time.Now()
	facts, err := mistralExtractor.ExtractFacts(personName, mockContent)
	duration := time.Since(startTime)

	t.Logf("Mistral API call duration: %s", duration)

	if err != nil {
		// LLM API calls can fail for various reasons (rate limits, content filters, etc.)
		t.Fatalf("ExtractFacts failed: %v", err)
	}

	if len(facts) == 0 {
		t.Fatalf("ExtractFacts returned zero facts. Expected at least one.")
	}

	t.Logf("Successfully extracted %d facts:", len(facts))
	for i, fact := range facts {
		t.Logf("  Fact %d:", i+1)
		t.Logf("    Normalized Fact: %s", fact.NormalizedFactText)
		t.Logf("    Original Quote: %s", fact.OriginalQuote)
		t.Logf("    Topics: %v", fact.Topics)
		t.Logf("    Involved People: %v", fact.InvolvedPeople)
		t.Logf("    Source URL: %s", fact.SourceURL)

		// Assertions
		if fact.NormalizedFactText == "" {
			t.Errorf("Fact %d has an empty normalized fact text", i+1)
		}
		if fact.OriginalQuote == "" {
			t.Errorf("Fact %d has an empty original quote", i+1)
		}
		if len(fact.InvolvedPeople) == 0 {
			t.Errorf("Fact %d has no involved people listed", i+1)
		}
		if fact.SourceURL != mockURL {
			t.Errorf("Fact %d has incorrect source URL: got %s, want %s", i+1, fact.SourceURL, mockURL)
		}
		// Basic check for topic presence (content depends heavily on LLM)
		if len(fact.Topics) == 0 {
			t.Logf("Warning: Fact %d has no topics listed.", i+1)
		}
	}
}
