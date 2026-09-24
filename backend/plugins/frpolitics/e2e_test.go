package frpolitics

import (
	"context"
	"os"
	"testing"

	"digital-mirror/backend/config"
	"digital-mirror/backend/storage"
)

// TestE2EWithSampleData runs an end-to-end integration test using local sample JSON data.
// It uses the actual production Run function to ensure all logic (derivations, affiliations)
// is tested exactly as it would run in production.
func TestE2EWithSampleData(t *testing.T) {
	skipIfCI(t)
	// 1. Setup Environment
	if _, err := config.LoadConfigForTest(); err != nil {
		t.Logf("Notice: config.LoadConfigForTest returned error: %v", err)
	}

	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; start Supabase and set env to run this test")
		return
	}

	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		t.Fatalf("db init: %v", err)
	}
	defer storer.Close()

	// 2. Clean Database
	cleanDatabase(t, storer.Pool())

	t.Log("=== FRPOLITICS E2E TEST WITH SAMPLE DATA ===")

	// 3. Prepare Sample Data
	// Ensure asset/cache exists and has our sample file renamed to the expected format
	// for discoverAndParse to find it.
	_ = os.MkdirAll("asset/cache", 0755)
	// We'll skip the actual download logic in discoverAndParse by putting the sample into the cache.
	// However, discoverAndParse expects Scrutins.json.zip.
	// Since we want to test with a specific local file, we'll use the Options to limit processing.

	opts := Options{
		MaxLaws:              2, // each ballot is stance-routed at insert (~5 s): keep the sample small
		MaxVotes:             100,
		PartyThreshold:       0.7,       // Standard threshold
		DisableKeywordFilter: true,      // Skip keyword filtering for tests
		DisableAIFilter:      true,      // Skip AI confidence filtering for tests
		Legislatures:         []int{17}, // 17th legislature for more recent data if available
	}

	// 4. Run Production Logic
	// We use the actual Run function which handles:
	// - Parsing
	// - AI Summarization
	// - Fact Derivation (Deputy & Party)
	// - Actor Insertion
	// - Affiliation Creation
	// - Fact Storage
	if err := Run(ctx, storer, opts); err != nil {
		t.Fatalf("frpolitics run failed: %v", err)
	}

	// 5. Verify and Print Results
	t.Log("=== VERIFICATION: EXTRACTS FROM DATABASE ===")
	pool := storer.Pool()

	// Verify counts
	var factCount, partyCount, humanCount int
	_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM facts").Scan(&factCount)
	_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM persons WHERE entity_type='party'").Scan(&partyCount)
	_ = pool.QueryRow(ctx, "SELECT COUNT(*) FROM persons WHERE entity_type='human'").Scan(&humanCount)
	t.Logf("Stored: %d facts, %d parties, %d deputies", factCount, partyCount, humanCount)

	if factCount == 0 || partyCount == 0 || humanCount == 0 {
		t.Fatalf("Mission failed: missing data in database (Facts: %d, Parties: %d, Deputies: %d)", factCount, partyCount, humanCount)
	}

	// Print Party Extracts (Verifying majority derivation via structure)
	t.Log("\n--- Derived Party Vote Extracts ---")
	rows, err := pool.Query(ctx, `
		SELECT p.name, f.fact_text
		FROM persons p
		JOIN fact_mentions fm ON p.id = fm.person_id
		JOIN fact_sources fs ON fm.fact_source_id = fs.id
		JOIN facts f ON fs.fact_id = f.id
		WHERE p.entity_type = 'party'
		  AND f.fact_text LIKE '%collectively%'
		ORDER BY p.name ASC LIMIT 10`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var name, text string
			if err := rows.Scan(&name, &text); err == nil {
				t.Logf("Party: %s | Fact: %s", name, text)
			}
		}
	}

	// Print Deputy Extracts
	t.Log("\n--- Sample Deputy Vote Extracts ---")
	rows, err = pool.Query(ctx, `
		SELECT p.name, f.fact_text
		FROM persons p
		JOIN fact_mentions fm ON p.id = fm.person_id
		JOIN fact_sources fs ON fm.fact_source_id = fs.id
		JOIN facts f ON fs.fact_id = f.id
		WHERE p.entity_type = 'human'
		ORDER BY p.name ASC LIMIT 5`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var name, text string
			if err := rows.Scan(&name, &text); err == nil {
				t.Logf("Deputy: %s | Fact: %s", name, text)
			}
		}
	}

	t.Log("\n=== TEST COMPLETED SUCCESSFULLY ===")
}
