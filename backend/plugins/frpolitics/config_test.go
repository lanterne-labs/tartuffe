package frpolitics

import (
	"context"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/config"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/require"
)

// TestConfigLoading tests the configuration loading system
func TestConfigLoading(t *testing.T) {
	// Load configuration for testing
	cfg, err := config.LoadConfigForTest()
	if err != nil {
		t.Skipf("Skipping test due to configuration loading error: %v", err)
		return
	}

	// Verify required fields are set
	require.NotEmpty(t, cfg.Database.URL, "Database URL should be set")
	require.NotEmpty(t, cfg.OpenAI.APIKey, "OpenAI API key should be set")
	require.NotEmpty(t, cfg.OpenAI.BaseURL, "OpenAI base URL should be set")

	t.Logf("Configuration loaded successfully:")
	t.Logf("  Database URL: %s", cfg.Database.URL)
	t.Logf("  OpenAI Base URL: %s", cfg.OpenAI.BaseURL)
	t.Logf("  OpenAI Chat Model: %s", cfg.OpenAI.ChatModel)
	t.Logf("  OpenAI Embed Model: %s", cfg.OpenAI.EmbedModel)
	t.Logf("  Frpolitics Max Laws: %d", cfg.Frpolitics.MaxLaws)
	t.Logf("  Frpolitics Max Votes: %d", cfg.Frpolitics.MaxVotes)
	t.Logf("  Frpolitics Party Threshold: %.2f", cfg.Frpolitics.PartyThreshold)
	t.Logf("  Frpolitics Legislatures: %v", cfg.Frpolitics.Legislatures)
}

// TestConfigBasedRealDataProcessing tests the full pipeline using configuration
func TestConfigBasedRealDataProcessing(t *testing.T) {
	// Load .env file from project root like the main application does
	if err := godotenv.Load("../../.env"); err != nil {
		t.Skipf("Skipping test due to .env loading error: %v", err)
		return
	}

	ctx := context.Background()

	// Initialize database connection using the same method as main application
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	defer storer.Close()

	t.Log("=== Running Config-Based Real Data Processing Test ===")

	// Configure options with test values
	since := time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC) // January 2023
	opts := Options{
		Since:          &since,
		MaxLaws:        2,         // Small dataset for testing
		MaxVotes:       50,        // Limited votes for testing
		PartyThreshold: 0.8,       // Standard threshold
		Legislatures:   []int{16}, // 16th legislature (2022-2024)
		RevisitHours:   10000,     // Force revisit for test
	}

	t.Logf("Running frpolitics with options: Since=%v, MaxLaws=%d, MaxVotes=%d, PartyThreshold=%.2f, Legislatures=%v",
		opts.Since, opts.MaxLaws, opts.MaxVotes, opts.PartyThreshold, opts.Legislatures)

	// Run the plugin
	err = Run(ctx, storer, opts)
	require.NoError(t, err)

	t.Log("=== Analyzing Results ===")

	// Get all facts from the database
	allFacts, err := storer.GetAllFactsGroupedByPerson(ctx)
	require.NoError(t, err)

	// Count facts by type
	deputyFacts := 0
	partyFacts := 0
	totalFacts := 0
	partyNames := make(map[string]int)
	deputyNames := make(map[string]int)

	for personName, facts := range allFacts {
		for range facts {
			totalFacts++

			// Classify as party or deputy fact
			lowerName := strings.ToLower(personName)
			if strings.Contains(lowerName, "parti") ||
				strings.Contains(lowerName, "groupe") ||
				strings.Contains(lowerName, "rassemblement") ||
				strings.Contains(lowerName, "socialiste") ||
				strings.Contains(lowerName, "républicain") ||
				strings.Contains(lowerName, "écologiste") ||
				strings.Contains(lowerName, "communiste") ||
				strings.Contains(lowerName, "democrate") ||
				strings.Contains(lowerName, "udf") ||
				strings.Contains(lowerName, "ump") ||
				strings.Contains(lowerName, "lr") ||
				strings.Contains(lowerName, "lrem") ||
				strings.Contains(lowerName, "rn") ||
				strings.Contains(lowerName, "fi") ||
				strings.Contains(lowerName, "ps") ||
				strings.Contains(lowerName, "eelv") {
				partyFacts++
				partyNames[personName]++
			} else {
				deputyFacts++
				deputyNames[personName]++
			}
		}
	}

	t.Logf("=== Summary ===")
	t.Logf("Total facts: %d", totalFacts)
	t.Logf("Deputy facts: %d", deputyFacts)
	t.Logf("Party facts: %d", partyFacts)
	t.Logf("Unique deputies: %d", len(deputyNames))
	t.Logf("Unique parties: %d", len(partyNames))

	// We should have both deputy and party facts
	require.Greater(t, totalFacts, 0, "Should have at least some facts")
	require.Greater(t, deputyFacts, 0, "Should have deputy facts")
	require.Greater(t, partyFacts, 0, "Should have party facts")

	// Print sample facts for verification
	t.Logf("=== Sample Facts ===")
	count := 0
	for personName, facts := range allFacts {
		if count >= 5 { // Limit output
			break
		}
		for _, fact := range facts {
			if count >= 5 {
				break
			}
			t.Logf("Person: %s | Fact: %s", personName, fact.FactText)
			count++
		}
	}

	t.Log("=== Config-Based Test Completed Successfully ===")
}

// TestDifferentThresholdsWithConfig tests how different thresholds affect party fact generation
func TestDifferentThresholdsWithConfig(t *testing.T) {
	// Load .env file from project root like the main application does
	if err := godotenv.Load("../../.env"); err != nil {
		t.Skipf("Skipping test due to .env loading error: %v", err)
		return
	}

	ctx := context.Background()

	// Initialize database connection
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	defer storer.Close()

	t.Log("=== Testing Different Thresholds with Config ===")

	thresholds := []float64{0.5, 0.7, 0.8, 0.9}

	for _, threshold := range thresholds {
		t.Logf("Testing with threshold %.1f", threshold)

		// Configure options
		since := time.Date(2023, time.January, 1, 0, 0, 0, 0, time.UTC)
		opts := Options{
			Since:          &since,
			MaxLaws:        1,  // Small dataset
			MaxVotes:       25, // Limited votes
			PartyThreshold: threshold,
			Legislatures:   []int{16}, // 16th legislature
			RevisitHours:   10000,
		}

		// Run plugin
		err = Run(ctx, storer, opts)
		require.NoError(t, err)

		// Count party facts
		allFacts, err := storer.GetAllFactsGroupedByPerson(ctx)
		require.NoError(t, err)

		partyFacts := 0
		for personName, facts := range allFacts {
			lowerName := strings.ToLower(personName)
			if strings.Contains(lowerName, "parti") ||
				strings.Contains(lowerName, "groupe") ||
				strings.Contains(lowerName, "rassemblement") ||
				strings.Contains(lowerName, "socialiste") ||
				strings.Contains(lowerName, "républicain") ||
				strings.Contains(lowerName, "écologiste") ||
				strings.Contains(lowerName, "communiste") ||
				strings.Contains(lowerName, "democrate") {
				partyFacts += len(facts)
			}
		}

		t.Logf("Threshold %.1f: %d party facts", threshold, partyFacts)
	}
}
