package search

import (
	// "fmt" // Removed unused import
	"net/url"
	"os"
	"strconv"
	"testing"

	"github.com/joho/godotenv"
)

// Helper function to load .env for tests
func loadEnv(t *testing.T) {
	t.Helper()
	// Construct the path to the .env file relative to the test file location.
	// Assumes .env is at the project root, and tests are in subdirs like backend/search.
	err := godotenv.Load("../../.env") // Adjust path as necessary
	if err != nil {
		t.Log("INFO: .env file not found or failed to load, tests needing env vars might be skipped.")
	}
}

// TestGoogleSearchProvider_Search tests the Google search functionality.
// It requires GOOGLE_API_KEY and GOOGLE_CSE_ID environment variables to be set (e.g., in .env).
// It uses TEST_GOOGLE_SEARCH_RESULT_LIMIT env var to control the number of results (default 5).
func TestGoogleSearchProvider_Search(t *testing.T) {
	loadEnv(t)

	apiKey := os.Getenv("GOOGLE_API_KEY")
	cseID := os.Getenv("GOOGLE_CSE_ID")

	if apiKey == "" || cseID == "" {
		t.Skip("Skipping Google Search test: GOOGLE_API_KEY or GOOGLE_CSE_ID not set.")
	}

	// Get result limit from env var, default to 5
	limitStr := os.Getenv("TEST_GOOGLE_SEARCH_RESULT_LIMIT")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 5 // Default limit
	}
	t.Logf("Using search result limit: %d", limit)

	provider, err := NewGoogleSearchProvider(apiKey, cseID)
	if err != nil {
		t.Fatalf("Failed to create GoogleSearchProvider: %v", err)
	}

	// Modify the Search function or create a test-specific version if we want to inject parameters
	// For now, we'll modify the base Search method temporarily for testing limit
	// A better approach might be to add options to the Search method itself.

	// Temporarily modify the Search function to accept limit - This is NOT ideal for production code
	// A better way: Add an options struct to the Search method.
	// For this test, we simulate by modifying the request URL generation within the test scope
	// (This requires exposing or refactoring parts of the original Search method, which we avoid for now)
	// Instead, we will rely on the fact that the Google API supports the 'num' parameter.
	// We need to modify the google.go Search method to accept/pass this parameter.

	// Let's refactor google.go's Search method to accept optional parameters
	// (This change will be done in a separate step)

	// Assuming Search is refactored to accept options:
	// opts := map[string]string{"num": strconv.Itoa(limit)}
	// results, err := provider.SearchWithOptions("Gabriel Attal", opts)

	// --- Placeholder: Calling original Search method for now --- 
	// We will address adding the limit parameter in the next step by modifying google.go
	personName := "Gabriel Attal"
	t.Logf("Performing search for: %s", personName)
	results, err := provider.Search(personName) // This call needs modification to support 'num'
	// --- End Placeholder ---

	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) == 0 {
		t.Log("Warning: Search returned 0 results.") // Don't fail, but log
	}

	// Because the 'num' parameter isn't implemented yet, we can't assert the exact limit.
	// We'll assert that we got *some* results (up to the API default, usually 10).
	// Once 'num' is added, we can assert len(results) <= limit.
	maxDefaultResults := 10
	if len(results) > maxDefaultResults {
		t.Errorf("Expected at most %d results (API default), but got %d", maxDefaultResults, len(results))
	}

	t.Logf("Received %d results.", len(results))
	for i, res := range results {
		if res.URL == "" {
			t.Errorf("Result %d has an empty URL", i+1)
		}
		if _, err := url.ParseRequestURI(res.URL); err != nil {
			t.Errorf("Result %d URL is invalid: %v", i+1, err)
		}
		// t.Logf("  %d: %s (%s)", i+1, res.Title, res.URL)
	}
}

// TestGoogleSearchProvider_SearchWithOptions tests the Google search functionality with options.
// It requires GOOGLE_API_KEY and GOOGLE_CSE_ID environment variables to be set (e.g., in .env).
// It uses TEST_GOOGLE_SEARCH_RESULT_LIMIT env var to control the number of results (default 5).
func TestGoogleSearchProvider_SearchWithOptions(t *testing.T) {
	loadEnv(t)

	apiKey := os.Getenv("GOOGLE_API_KEY")
	cseID := os.Getenv("GOOGLE_CSE_ID")

	if apiKey == "" || cseID == "" {
		t.Skip("Skipping Google Search test: GOOGLE_API_KEY or GOOGLE_CSE_ID not set.")
	}

	// Get result limit from env var, default to 5
	limitStr := os.Getenv("TEST_GOOGLE_SEARCH_RESULT_LIMIT")
	limit, err := strconv.Atoi(limitStr)
	if err != nil || limit <= 0 {
		limit = 5 // Default limit
	}
	if limit > 10 { // Google API max limit is 10 for 'num'
		t.Logf("Warning: Requested limit %d exceeds Google API max of 10. Using 10.", limit)
		limit = 10
	}
	t.Logf("Using search result limit: %d", limit)

	provider, err := NewGoogleSearchProvider(apiKey, cseID)
	if err != nil {
		t.Fatalf("Failed to create GoogleSearchProvider: %v", err)
	}

	personName := "Gabriel Attal"
	options := map[string]string{"num": strconv.Itoa(limit)}

	t.Logf("Performing search for: %s with options %v", personName, options)
	results, err := provider.SearchWithOptions(personName, options)

	if err != nil {
		t.Fatalf("SearchWithOptions failed: %v", err)
	}

	if len(results) == 0 {
		t.Log("Warning: Search returned 0 results.") // Don't fail, but log
	}

	// Assert that the number of results does not exceed the requested limit
	if len(results) > limit {
		t.Errorf("Expected at most %d results, but got %d", limit, len(results))
	}

	t.Logf("Received %d results (limit was %d).", len(results), limit)
	for i, res := range results {
		if res.URL == "" {
			t.Errorf("Result %d has an empty URL", i+1)
		}
		if _, err := url.ParseRequestURI(res.URL); err != nil {
			t.Errorf("Result %d URL is invalid: %v", i+1, err)
		}
		// t.Logf("  %d: %s (%s)", i+1, res.Title, res.URL)
	}
} 