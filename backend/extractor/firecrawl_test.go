package extractor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFirecrawlExtractor_ExtractContent_Live(t *testing.T) {
	// Load .env file from the project root
	// Assumes the .env file is two levels up from the current test file (e.g., backend/extractor -> .env)
	err := godotenv.Load(filepath.Join("..", "..", ".env"))
	if err != nil {
		t.Logf("Could not load .env file, proceeding with environment variables: %v", err)
	}

	apiKey := os.Getenv("FIRECRAWL_API_KEY")
	if apiKey == "" {
		t.Skip("FIRECRAWL_API_KEY not set in environment or .env file, skipping live test.")
	}

	extractor, err := NewFirecrawlExtractorFromEnv()
	require.NoError(t, err, "NewFirecrawlExtractorFromEnv() should not return an error when API key is set")
	require.NotNil(t, extractor, "Extractor should not be nil when API key is set")

	// A simple, reliable URL for testing
	testURL := "https://www.example.com"

	t.Logf("Attempting to extract content from: %s using Firecrawl", testURL)
	extractedContent, err := extractor.ExtractContent(testURL)

	// Assertions
	assert.NoError(t, err, "ExtractContent should not return an error for a valid URL and API key")
	assert.NotNil(t, extractedContent, "ExtractedContent should not be nil on successful extraction")

	if extractedContent != nil {
		assert.NotEmpty(t, extractedContent.Content, "Extracted content (Markdown) should not be empty")
		assert.NotEmpty(t, extractedContent.Title, "Extracted title should not be empty")
		assert.Equal(t, testURL, extractedContent.URL, "Extracted URL should match the input URL")
		t.Logf("Successfully extracted title: %s", extractedContent.Title)
		t.Logf("Successfully extracted content (first 100 chars): %.100s...", extractedContent.Content)
	}
}
