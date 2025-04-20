package content

import (
	"os"
	"testing"

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

// TestApifyExtractor_ExtractContent tests the Apify content extraction.
// It requires APIFY_API_KEY environment variable to be set (e.g., in .env).
// Uses a static, simple URL for cost-effectiveness and reliability.
func TestApifyExtractor_ExtractContent(t *testing.T) {
	loadEnv(t)

	apiKey := os.Getenv("APIFY_API_KEY")
	if apiKey == "" {
		t.Skip("Skipping Apify Extraction test: APIFY_API_KEY not set.")
	}

	extractor, err := NewApifyExtractor(apiKey)
	if err != nil {
		t.Fatalf("Failed to create ApifyExtractor: %v", err)
	}

	// Use a static, simple URL for testing to avoid depending on search and reduce cost
	// Simple English Wikipedia page is usually straightforward to scrape.
	testURL := "https://simple.wikipedia.org/wiki/Web_scraping"
	t.Logf("Attempting to extract content from: %s", testURL)

	// TODO: Configure Apify actor input for minimal cost if possible.
	// For website-content-crawler, defaults might be okay, but consider options like:
	// - Setting maxDepth=0 if using startUrls (though we use sync API here)
	// - Explicitly requesting outputFormat="text" if Markdown is more expensive/not needed for test.
	// For now, we use the default synchronous run input.

	extractedContent, err := extractor.ExtractContent(testURL)
	if err != nil {
		// Apify synchronous runs can be slow, give a clear error message.
		t.Fatalf("ExtractContent failed for %s: %v. (Note: Apify sync runs can take > 60s)", testURL, err)
	}

	if extractedContent == nil {
		t.Fatalf("ExtractContent returned nil content for %s", testURL)
	}

	t.Logf("Successfully extracted content. Title: '%s', URL: %s", extractedContent.Title, extractedContent.URL)

	// Basic assertions
	if extractedContent.URL != testURL {
		// Note: Apify might return a canonicalized URL, so this check might be too strict.
		// t.Logf("Warning: Extracted URL '%s' differs from input URL '%s'", extractedContent.URL, testURL)
	}
	if extractedContent.Content == "" {
		t.Errorf("Extracted content is empty for %s", testURL)
	}
	if len(extractedContent.Content) < 50 { // Expecting more than a few characters
		t.Errorf("Extracted content seems too short (%d chars) for %s", len(extractedContent.Content), testURL)
	}

	// Check if title was extracted (might be empty depending on actor/page)
	if extractedContent.Title == "" {
		t.Logf("Warning: Extracted title is empty for %s", testURL)
	}

} 