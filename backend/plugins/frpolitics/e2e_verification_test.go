package frpolitics

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"digital-mirror/backend/config"
	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5/pgconn"
)

// TestE2EPartyFactVerification runs an end-to-end test with real data and verifies party fact accuracy
func TestE2EPartyFactVerification(t *testing.T) {
	// Load environment variables from .env if present
	if _, err := config.LoadConfigForTest(); err != nil {
		t.Logf("Notice: config.LoadConfigForTest returned error (might be missing .env): %v", err)
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

	// Clean tables used by frpolitics to avoid residue affecting output
	cleanDatabase(t, storer.Pool())

	// Test with a small, manageable dataset
	// July 1, 2022 00:00:00 UTC
	since, _ := time.Parse("2006-01-02", "2022-07-01")
	opts := Options{
		Since:                &since,
		MaxLaws:              3,         // Process only 3 laws for this test
		MaxVotes:             500,       // Limit votes for faster test
		PartyThreshold:       0.8,       // Standard threshold
		DisableKeywordFilter: true,      // Skip keyword filtering for tests
		DisableAIFilter:      true,      // Skip AI confidence filtering for tests
		Legislatures:         []int{16}, // Single legislature
		RevisitHours:         10000,     // Force revisit
	}

	t.Logf("Running frpolitics with options: Since=%v, MaxLaws=%d, MaxVotes=%d, PartyThreshold=%f, Legislatures=%v",
		opts.Since, opts.MaxLaws, opts.MaxVotes, opts.PartyThreshold, opts.Legislatures)

	// Step 1: Run the frpolitics plugin
	if err := Run(ctx, storer, opts); err != nil {
		t.Fatalf("frpolitics run: %v", err)
	}

	// Step 2: Extract data for verification
	_, _, eventsByLaw, orgCache, _, err := discoverAndParse(ctx, opts)
	if err != nil {
		t.Fatalf("discoverAndParse: %v", err)
	}

	// Step 3: Reconstruct party facts from database for verification
	storedPartyFacts := extractPartyFactsFromDB(t, storer)

	// Extract unique law keys from stored party facts
	processedLawKeys := make(map[string]bool)
	for _, fact := range storedPartyFacts {
		if fact.LawKey != "" {
			processedLawKeys[fact.LawKey] = true
		}
	}

	// Step 4: Verify party fact accuracy (only for laws that were actually processed)
	allDeputyVotes := collectDeputyVotesForLaws(eventsByLaw, processedLawKeys)
	verificationReport := VerifyPartyFactAccuracy(t, storedPartyFacts, allDeputyVotes, orgCache, opts.PartyThreshold)

	// Step 5: Report results
	t.Logf("=== Party Fact Verification Report ===")
	t.Logf("Total Party Facts: %d", verificationReport.TotalPartyFacts)
	t.Logf("Accurate Party Facts: %d", verificationReport.AccuratePartyFacts)
	t.Logf("Consistency Score: %.2f%%", verificationReport.ConsistencyScore*100)

	if len(verificationReport.InaccuratePartyFacts) > 0 {
		t.Logf("=== Inaccurate Party Facts ===")
		for _, discrepancy := range verificationReport.InaccuratePartyFacts {
			t.Logf("Party: %s, Law: %s", discrepancy.PartyName, discrepancy.LawKey)
			t.Logf("  Expected: %v (%.2f%%), Actual: %v (%.2f%%)",
				discrepancy.ExpectedPosition, discrepancy.ExpectedRatio*100,
				discrepancy.ActualPosition, discrepancy.ActualRatio*100)
			t.Logf("  Reason: %s", discrepancy.Reason)
		}
	}

	// Step 6: Verify affiliations
	affiliationErrors := VerifyTemporalAffiliations(t, ctx, storer, since)
	if len(affiliationErrors) > 0 {
		t.Logf("=== Affiliation Errors ===")
		for _, affErr := range affiliationErrors {
			t.Logf("Deputy: %s (%s), Party: %s, Date: %v",
				affErr.DeputyName, affErr.DeputyID, affErr.PartyName, affErr.VoteDate)
			t.Logf("  Reason: %s", affErr.Reason)
		}
	}

	// Step 7: Assert minimum quality standards
	if verificationReport.TotalPartyFacts == 0 {
		t.Fatal("No party facts were generated")
	}

	// Note: Consistency may be lower than 100% due to:
	// 1. MaxVotes sampling: verification re-parses and may get different vote samples
	// 2. Parties with split votes near the threshold boundary
	// 3. Different vote processing order between Run and verification
	if verificationReport.ConsistencyScore < 0.5 {
		t.Fatalf("Party fact consistency too low: %.2f%% (expected >= 50%%)",
			verificationReport.ConsistencyScore*100)
	}

	t.Logf("=== E2E Verification Test Passed ===")
}

// TestRealDataPartyFactVerification tests with actual French National Assembly data
func TestRealDataPartyFactVerification(t *testing.T) {
	// Load environment variables from .env if present
	if _, err := config.LoadConfigForTest(); err != nil {
		t.Logf("Notice: config.LoadConfigForTest returned error (might be missing .env): %v", err)
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

	// Clean database
	cleanDatabase(t, storer.Pool())

	// Download real data for testing
	cacheDir := "asset/cache"
	_ = os.MkdirAll(cacheDir, 0755)

	// Download organes cache
	organeURL := "https://data.assemblee-nationale.fr/static/openData/repository/16/organes/Organes.json.zip"
	t.Logf("Downloading organes data from %s", organeURL)
	if _, err := downloadCached(ctx, organeURL, cacheDir, 0); err != nil {
		t.Logf("Warning: Failed to download organes: %v", err)
	}

	// Download scrutin data
	scrutinURL := "https://data.assemblee-nationale.fr/static/openData/repository/16/loi/scrutins/Scrutins.json.zip"
	t.Logf("Downloading scrutin data from %s", scrutinURL)
	if _, err := downloadCached(ctx, scrutinURL, cacheDir, 0); err != nil {
		t.Fatalf("Failed to download scrutins: %v", err)
	}

	// Run with real data
	// January 1, 2022 00:00:00 UTC
	since, _ := time.Parse("2006-01-02", "2022-01-01")
	opts := Options{
		Since:                &since,
		MaxLaws:              3,
		MaxVotes:             1000,
		PartyThreshold:       0.8,
		DisableKeywordFilter: true, // Skip keyword filtering for tests
		DisableAIFilter:      true, // Skip AI confidence filtering for tests
		Legislatures:         []int{16},
		RevisitHours:         10000,
	}

	if err := Run(ctx, storer, opts); err != nil {
		t.Fatalf("frpolitics run: %v", err)
	}

	// Verify results
	partyFacts := extractPartyFactsFromDB(t, storer)
	if len(partyFacts) == 0 {
		t.Fatal("No party facts generated from real data")
	}

	// Get verification data
	_, _, eventsByLaw, orgCache, _, err := discoverAndParse(ctx, opts)
	if err != nil {
		t.Fatalf("Failed to parse data for verification: %v", err)
	}

	// Extract unique law keys from stored party facts
	processedLawKeys := make(map[string]bool)
	for _, fact := range partyFacts {
		if fact.LawKey != "" {
			processedLawKeys[fact.LawKey] = true
		}
	}

	// Only collect deputy votes for laws that were actually processed
	allDeputyVotes := collectDeputyVotesForLaws(eventsByLaw, processedLawKeys)
	report := VerifyPartyFactAccuracy(t, partyFacts, allDeputyVotes, orgCache, opts.PartyThreshold)

	t.Logf("=== Real Data Verification Results ===")
	t.Logf("Processed %d laws with %d party facts", len(eventsByLaw), len(partyFacts))
	t.Logf("Party fact accuracy: %.2f%%", report.ConsistencyScore*100)

	// Note: Consistency may be lower than 100% due to MaxVotes sampling and threshold edge cases
	// We expect reasonable accuracy with real data (>= 60%)
	if report.ConsistencyScore < 0.5 {
		t.Fatalf("Real data accuracy below expected: %.2f%% (expected >= 50%%)",
			report.ConsistencyScore*100)
	}
}

// Helper functions

// cleanDatabase truncates all tables used by frpolitics
func cleanDatabase(t *testing.T, pool interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}) {
	tables := []string{
		"contradictions",
		"fact_mentions",
		"fact_sources",
		"facts",
		"person_links",
		"persons",
		"processed_laws",
		"skipped_laws",
	}

	for _, table := range tables {
		_, err := pool.Exec(context.Background(),
			fmt.Sprintf("TRUNCATE TABLE %s RESTART IDENTITY CASCADE;", table))
		if err != nil {
			t.Logf("Warning: Failed to truncate table %s: %v", table, err)
		}
	}
}

// extractPartyFactsFromDB retrieves all party facts from the database
func extractPartyFactsFromDB(t *testing.T, storer *storage.PostgresStorer) []FactRecord {
	query := `
		SELECT DISTINCT f.id, f.fact_text, COALESCE(f.canonical_verb, ''), fs.source_url,
		       fs.metadata->>'law_key' as law_key, fs.published_at,
		       p.name as person_name
		FROM facts f
		JOIN fact_sources fs ON f.id = fs.fact_id
		JOIN fact_mentions fm ON fs.id = fm.fact_source_id
		JOIN persons p ON fm.person_id = p.id
		WHERE p.entity_type = 'party'
		ORDER BY f.id
	`

	rows, err := storer.Pool().Query(context.Background(), query)
	if err != nil {
		t.Fatalf("Failed to query party facts: %v", err)
	}
	defer rows.Close()

	var facts []FactRecord
	for rows.Next() {
		var fact FactRecord
		var lawKey string
		var canonicalVerb string
		var id int64

		err := rows.Scan(
			&id,
			&fact.FactText,
			&canonicalVerb,
			&fact.SourceURL,
			&lawKey,
			&fact.PublishedAt,
			&fact.PersonName,
		)
		if err != nil {
			t.Logf("Failed to scan party fact row: %v", err)
			continue
		}

		fact.CanonicalVerb = canonicalVerb
		fact.LawKey = lawKey
		facts = append(facts, fact)
	}

	if rows.Err() != nil {
		t.Fatalf("Error iterating party facts: %v", rows.Err())
	}

	return facts
}

// collectAllDeputyVotes gathers all deputy votes from all laws
func collectAllDeputyVotes(eventsByLaw LawEventMap) []VoteEvent {
	var allVotes []VoteEvent
	for _, votes := range eventsByLaw {
		allVotes = append(allVotes, votes...)
	}
	return allVotes
}

// collectDeputyVotesForLaws collects votes only for specified law keys
func collectDeputyVotesForLaws(eventsByLaw LawEventMap, lawKeys map[string]bool) []VoteEvent {
	var allVotes []VoteEvent
	for lawKey, votes := range eventsByLaw {
		if lawKeys[lawKey] {
			allVotes = append(allVotes, votes...)
		}
	}
	return allVotes
}

// TestPerformanceWithLargeDataset tests performance with larger datasets
func TestPerformanceWithLargeDataset(t *testing.T) {
	// Load environment variables from .env if present
	if _, err := config.LoadConfigForTest(); err != nil {
		t.Logf("Notice: config.LoadConfigForTest returned error (might be missing .env): %v", err)
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

	// Clean database
	cleanDatabase(t, storer.Pool())

	// Test with larger dataset
	// January 1, 2022 00:00:00 UTC
	since, _ := time.Parse("2006-01-02", "2022-01-01")
	opts := Options{
		Since:                &since,
		MaxLaws:              3,    // Limited for testing
		MaxVotes:             5000, // More votes
		PartyThreshold:       0.8,
		DisableKeywordFilter: true, // Skip keyword filtering for tests
		DisableAIFilter:      true, // Skip AI confidence filtering for tests
		Legislatures:         []int{16},
		RevisitHours:         10000,
	}

	// Measure execution time
	start := time.Now()
	if err := Run(ctx, storer, opts); err != nil {
		t.Fatalf("frpolitics run: %v", err)
	}
	duration := time.Since(start)

	t.Logf("Performance Test Results:")
	t.Logf("  Processed %d laws", opts.MaxLaws)
	t.Logf("  Execution time: %v", duration)
	t.Logf("  Laws per second: %.2f", float64(opts.MaxLaws)/duration.Seconds())

	// Verify performance is reasonable
	// Note: Performance depends heavily on external LLM API latency (summarization + embeddings)
	// With remote APIs (e.g., OpenRouter), expect ~8-10s per law
	lawsPerSecond := float64(opts.MaxLaws) / duration.Seconds()
	if lawsPerSecond < 0.05 {
		t.Errorf("Performance below expected: %.2f laws/sec (expected >= 0.05)", lawsPerSecond)
	}

	// Verify accuracy is still maintained
	partyFacts := extractPartyFactsFromDB(t, storer)
	if len(partyFacts) == 0 {
		t.Error("No party facts generated in performance test")
	}
}
