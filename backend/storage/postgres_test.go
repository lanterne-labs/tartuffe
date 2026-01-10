package storage

import (
	"context"
	"digital-mirror/backend/util"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	// "github.com/jackc/pgx/v5/pgxpool" // Removed unused import
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/joho/godotenv"
	"github.com/pgvector/pgvector-go"    // Import pgvector
	"github.com/stretchr/testify/assert" // Using testify for clearer assertions
	"github.com/stretchr/testify/require"
)

// embeddingDimension defines the dimension for mock embeddings
const embeddingDimension = 1024 // Match Mistral dimension

// Helper function to load .env for tests
func loadEnv(t *testing.T) {
	t.Helper()
	err := godotenv.Load("../../.env")
	if err != nil {
		// Allow tests to run if OPEN_AI key is set directly in env
		if os.Getenv("OPEN_AI_KEY") == "" || os.Getenv("DATABASE_URL") == "" {
			t.Log("INFO: .env file not found and OPEN_AI_KEY or DATABASE_URL not set in env, tests needing them might be skipped.")
		}
	}
}

// setupTestDB connects to the test database and returns a storer and a teardown function.
func setupTestDB(t *testing.T) (*PostgresStorer, func()) {
	loadEnv(t)
	dbURL := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, dbURL, "DATABASE_URL must be set for storage tests")
	key := os.Getenv("OPEN_AI_KEY")
	require.NotEmpty(t, key, "OPEN_AI_KEY must be set for storage tests") // Needed for embedder

	ctx := context.Background()

	// Storer no longer needs embedder
	storer, err := NewPostgresStorerFromEnv(ctx)
	if err != nil {
		t.Fatalf("Failed to connect to test database: %v", err)
	}

	// Teardown function to close the pool
	teardown := func() {
		storer.Close()
	}

	// Clean up relevant tables before running the test
	// Use TRUNCATE ... RESTART IDENTITY CASCADE for clean slate
	_, err = storer.pool.Exec(ctx, "TRUNCATE TABLE fact_mentions, facts, fact_sources, person_links, persons, contradictions RESTART IDENTITY CASCADE")
	require.NoError(t, err, "Failed to truncate tables")

	// Ensure the unique index required for upsert exists for tests.
	_, err = storer.pool.Exec(ctx, "CREATE UNIQUE INDEX IF NOT EXISTS uq_person_links_src_dst_link ON person_links (src_person_id, dst_person_id, link_type);")
	require.NoError(t, err, "Failed to ensure unique index on person_links for tests")

	return storer, teardown
}

// Helper to generate a mock embedding vector
func mockEmbedding() []float32 {
	embedding := make([]float32, embeddingDimension)
	for i := range embedding {
		embedding[i] = rand.Float32()
	}
	return embedding
}

// TestNormalizePersonName tests the name normalization logic.
func TestNormalizePersonName(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{" Person A ", "person a"},
		{"person b", "person b"},
		{"PERSON C", "person c"},
		{"  Multiple   Spaces  ", "multiple   spaces"}, // Note: Internal spaces are kept
		{"", ""},
		{"   ", ""},
	}

	for _, tc := range testCases {
		tc := tc // Capture range variable
		t.Run(fmt.Sprintf("Input_%s", tc.input), func(t *testing.T) {
			assert.Equal(t, tc.expected, util.NormalizeString(tc.input))
		})
	}
}

// TestPostgresStorer_StoreFact tests inserting a new fact, source, persons, and mentions.
func TestPostgresStorer_StoreFact(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()

	ctx := context.Background()
	pubDate := time.Date(2024, 1, 15, 10, 0, 0, 0, time.UTC)
	input := StoreFactInput{
		NormalizedFactText: "Person A and Person B agree on Policy X.",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "In a joint statement, Person A and B declared their full support for Policy X.",
		Topics:             []string{"policy", "agreement"},
		InvolvedPeople:     []string{" Person A ", "Person B"}, // Test normalization
		SourceURL:          "http://example.com/policy-agreement-1",
		PublishedAt:        &pubDate,
		SourceType:         "test",
		Metadata:           map[string]any{"k": "v"},
	}

	// --- Action: Store the fact ---
	// Handle 4 return values, ignore personIDs for this test
	factID, factSourceID, _, err := storer.StoreFact(ctx, input)
	require.NoError(t, err, "StoreFact failed")
	require.Positive(t, factID, "StoreFact should return a positive factID")
	require.Positive(t, factSourceID, "StoreFact should return a positive factSourceID")

	// --- Verification ---
	var queriedFactText string
	var queriedEmbedding pgvector.Vector
	var queriedTopics []string

	// 1. Verify the fact exists in 'facts' table
	factQuery := "SELECT fact_text, embedding, topics FROM facts WHERE id = $1"
	err = storer.pool.QueryRow(ctx, factQuery, factID).Scan(
		&queriedFactText, &queriedEmbedding, &queriedTopics,
	)
	require.NoError(t, err, "Failed to query inserted fact by ID")
	assert.Equal(t, input.NormalizedFactText, queriedFactText)
	assert.Equal(t, input.Topics, queriedTopics)
	require.NotNil(t, queriedEmbedding, "Embedding should not be nil in DB")
	// Compare float slices carefully
	assert.InDeltaSlice(t, input.Embedding, queriedEmbedding.Slice(), 0.00001, "Embedding mismatch")

	// 2. Verify the source exists in 'fact_sources' table
	var queriedFactID, queriedSourceURL, queriedOriginalQuote, queriedSourceType string
	var queriedPublishedAt *time.Time
	sourceQuery := "SELECT fact_id, source_url, original_quote, published_at, source_type FROM fact_sources WHERE id = $1"
	err = storer.pool.QueryRow(ctx, sourceQuery, factSourceID).Scan(
		&queriedFactID, &queriedSourceURL, &queriedOriginalQuote, &queriedPublishedAt, &queriedSourceType,
	)
	require.NoError(t, err, "Failed to query inserted fact source by ID")
	assert.Equal(t, fmt.Sprintf("%d", factID), queriedFactID, "fact_sources.fact_id does not match returned factID") // Compare as string due to potential type mismatch (BIGINT vs int64)
	assert.Equal(t, input.SourceURL, queriedSourceURL)
	assert.Equal(t, input.OriginalQuote, queriedOriginalQuote)
	require.NotNil(t, queriedPublishedAt, "PublishedAt should not be nil in DB")
	assert.WithinDuration(t, *input.PublishedAt, *queriedPublishedAt, time.Second, "PublishedAt mismatch in fact_sources")

	// 3. Verify the persons exist and get their IDs
	personIDs := make(map[string]int64) // Map normalized name to ID
	normalizedNamesForQuery := []string{"person a", "person b"}
	personsQuery := "SELECT id, name FROM persons WHERE name = ANY($1::text[])"
	rows, err := storer.pool.Query(ctx, personsQuery, normalizedNamesForQuery)
	require.NoError(t, err, "Failed to query persons")
	personCount := 0
	for rows.Next() {
		var personID int64
		var personName string
		err = rows.Scan(&personID, &personName)
		require.NoError(t, err, "Failed to scan person row")
		personIDs[personName] = personID
		personCount++
	}
	require.NoError(t, rows.Err(), "Error iterating person rows")
	require.Equal(t, len(normalizedNamesForQuery), personCount, "Incorrect number of persons found")
	rows.Close()

	// 4. Verify the fact_mentions links exist
	mentionsQuery := "SELECT person_id FROM fact_mentions WHERE fact_source_id = $1"
	mentionRows, err := storer.pool.Query(ctx, mentionsQuery, factSourceID)
	require.NoError(t, err, "Failed to query fact_mentions")
	mentionCount := 0
	foundPersonIDs := make(map[int64]bool)
	for mentionRows.Next() {
		var personID int64
		err = mentionRows.Scan(&personID)
		require.NoError(t, err, "Failed to scan mention row")
		foundPersonIDs[personID] = true
		mentionCount++
	}
	require.NoError(t, mentionRows.Err(), "Error iterating mention rows")
	assert.Equal(t, len(normalizedNamesForQuery), mentionCount, "Incorrect number of mentions found")
	mentionRows.Close()

	// Check if the found mention person IDs match the inserted person IDs
	for _, expectedID := range personIDs {
		assert.True(t, foundPersonIDs[expectedID], "Mention link missing for person ID %d", expectedID)
	}
}

// TestPostgresStorer_StoreFact_Idempotency_And_SourceUpdate tests:
// 1. Storing the exact same fact (same text) twice fails due to unique constraint on facts.fact_text.
// 2. Storing a *different* fact from an *existing source URL* updates the fact_sources record (fact_id and last_visited_at).
func TestPostgresStorer_StoreFact_Idempotency_And_SourceUpdate(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	commonSourceURL := "http://example.com/idempotent-and-update"
	pubDate1 := time.Now().Add(-2 * time.Hour)

	input1 := StoreFactInput{
		NormalizedFactText: "Idempotency test fact one.",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Original quote for fact one.",
		Topics:             []string{"idempotency", "update"},
		InvolvedPeople:     []string{"Updater Person"},
		SourceURL:          commonSourceURL,
		PublishedAt:        &pubDate1,
	}

	// --- Action: Store the fact first time ---
	factID1, factSourceID1, _, err := storer.StoreFact(ctx, input1)
	require.NoError(t, err, "First StoreFact failed")
	require.Positive(t, factID1)
	require.Positive(t, factSourceID1)

	// --- Part 1: Test Idempotency for exact same fact text ---
	input1Attempt2 := input1 // Exact same input
	input1Attempt2.OriginalQuote = "Slightly different quote, but same fact text."
	_, _, _, err = storer.StoreFact(ctx, input1Attempt2)
	require.Error(t, err, "Second StoreFact with exact same fact_text should fail")
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		assert.Equal(t, "23505", pgErr.Code, "Expected PostgreSQL unique violation on facts.fact_text (23505)")
		assert.Contains(t, pgErr.ConstraintName, "facts_fact_text_key", "Error message should mention facts_fact_text_key constraint")
	} else {
		t.Logf("Warning: Could not assert specific PostgreSQL error type for fact_text unique violation. Error: %v", err)
		assert.ErrorContains(t, err, "duplicate key value violates unique constraint") // Generic check
	}

	// --- Part 2: Test storing a *different* fact from the *same source URL* ---
	// The original fact_sources record (factSourceID1) should be updated.
	pubDate2 := time.Now().Add(-1 * time.Hour)
	input2 := StoreFactInput{
		NormalizedFactText: "A completely different fact two.", // Different fact text
		Embedding:          mockEmbedding(),                    // Different embedding
		OriginalQuote:      "Quote for the second, different fact.",
		Topics:             []string{"new topic"},
		InvolvedPeople:     []string{"Updater Person", "New Person"}, // Can have different people
		SourceURL:          commonSourceURL,                          // Same source URL as input1
		PublishedAt:        &pubDate2,
	}

	factID2, factSourceID2, _, err := storer.StoreFact(ctx, input2)
	require.NoError(t, err, "StoreFact for a different fact from same URL failed")
	require.Positive(t, factID2, "factID2 should be positive")
	require.NotEqual(t, factID1, factID2, "factID1 and factID2 should be different")

	// Critical: The fact_sources ID should be the *same* because the source_url is the same, and it should have been updated.
	assert.Equal(t, factSourceID1, factSourceID2, "fact_source_id should be the same as the first insert, indicating an update")

	// Verify the fact_sources table for this commonSourceURL
	var updatedFactID string // pgx scans int8 as string
	var updatedOriginalQuote string
	var updatedPublishedAt time.Time
	var updatedLastVisitedAt time.Time

	sourceQuery := "SELECT fact_id, original_quote, published_at, last_visited_at FROM fact_sources WHERE source_url = $1"
	err = storer.pool.QueryRow(ctx, sourceQuery, commonSourceURL).Scan(
		&updatedFactID, &updatedOriginalQuote, &updatedPublishedAt, &updatedLastVisitedAt,
	)
	require.NoError(t, err, "Failed to query updated fact_sources record")

	assert.Equal(t, fmt.Sprintf("%d", factID2), updatedFactID, "fact_sources.fact_id should point to the new factID2")
	assert.Equal(t, input2.OriginalQuote, updatedOriginalQuote, "fact_sources.original_quote should be updated")
	assert.WithinDuration(t, *input2.PublishedAt, updatedPublishedAt, time.Second, "fact_sources.published_at should be updated")
	assert.WithinDuration(t, time.Now(), updatedLastVisitedAt, 2*time.Second, "fact_sources.last_visited_at should be updated to current time")

	// Verify that the first fact (factID1) still exists, but the source is no longer primarily linked to it.
	var fact1Exists bool
	err = storer.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM facts WHERE id = $1)", factID1).Scan(&fact1Exists)
	require.NoError(t, err)
	assert.True(t, fact1Exists, "The first fact (factID1) should still exist in the facts table")
}

// TestFindOrCreateAffiliation verifies the ability to create and query an affiliation link
func TestFindOrCreateAffiliation(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	// Create two persons
	pNameA := "Deputy Person A"
	pNameB := "Party B"
	pIDA, _, err := storer.AddPerson(ctx, pNameA)
	require.NoError(t, err)
	pIDB, _, err := storer.AddPerson(ctx, pNameB)
	require.NoError(t, err)

	// Start a transaction to upsert affiliation
	tx, err := storer.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Create an affiliation with valid_from and NULL valid_to
	vf := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	err = storer.FindOrCreateAffiliation(ctx, tx, pIDA, pIDB, &vf, nil, map[string]any{"source": "actor_dataset"})
	require.NoError(t, err)

	// Commit
	require.NoError(t, tx.Commit(ctx))

	// Query the person_links table to confirm presence
	var found int
	err = storer.pool.QueryRow(ctx, `SELECT COUNT(*) FROM person_links WHERE src_person_id = $1 AND dst_person_id = $2 AND link_type = 'affiliation'`, pIDA, pIDB).Scan(&found)
	require.NoError(t, err)
	assert.Equal(t, 1, found)
}

// TestGetAffiliatedPersonIDs verifies the date-aware affiliation query helper.
func TestGetAffiliatedPersonIDs(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	// Create deputy and two party persons
	deputyName := "Deputy DateTest"
	party1 := "Party One"
	party2 := "Party Two"
	depID, _, err := storer.AddPerson(ctx, deputyName)
	require.NoError(t, err)
	p1ID, _, err := storer.AddPerson(ctx, party1)
	require.NoError(t, err)
	p2ID, _, err := storer.AddPerson(ctx, party2)
	require.NoError(t, err)

	tx, err := storer.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)

	// Insert two affiliations with different date ranges
	v1 := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	v2 := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
	// first: party1 from 2020-01-01 (open ended)
	require.NoError(t, storer.FindOrCreateAffiliation(ctx, tx, depID, p1ID, &v1, nil, map[string]any{"source": "actor_dataset"}))
	// second: party2 from 2022-01-01 to 2022-12-31
	end := time.Date(2022, 12, 31, 0, 0, 0, 0, time.UTC)
	require.NoError(t, storer.FindOrCreateAffiliation(ctx, tx, depID, p2ID, &v2, &end, map[string]any{"source": "actor_dataset"}))

	require.NoError(t, tx.Commit(ctx))

	// Query affiliations at different dates to verify correctness
	tx2, err := storer.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx2.Rollback(ctx)

	// Date 2021 -> should be affiliated with party1 only
	date2021 := time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC)
	ids2021, err := storer.GetAffiliatedPersonIDs(ctx, tx2, depID, &date2021)
	require.NoError(t, err)
	assert.Equal(t, 1, len(ids2021))
	assert.Equal(t, p1ID, ids2021[0])

	// Date 2022 -> party1 and party2 (since p2 starts in 2022 and p1 is open-ended)
	date2022 := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
	ids2022, err := storer.GetAffiliatedPersonIDs(ctx, tx2, depID, &date2022)
	require.NoError(t, err)
	// Can be in any order, so check both present
	assert.Contains(t, ids2022, p1ID)
	assert.Contains(t, ids2022, p2ID)

	// Date 2023 -> party2 ended, so only party1 should remain
	date2023 := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
	ids2023, err := storer.GetAffiliatedPersonIDs(ctx, tx2, depID, &date2023)
	require.NoError(t, err)
	assert.Equal(t, 1, len(ids2023))
	assert.Equal(t, p1ID, ids2023[0])

	require.NoError(t, tx2.Rollback(ctx))
}

// TestStoreFact_CrossAttachAffiliations ensures that if a deputy has an affiliation, storing a fact for the deputy
// also creates a mention for the affiliated party when the fact's PublishedAt is within the affiliation window.
func TestStoreFact_CrossAttachAffiliations(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	// Create deputy and party
	deputy := "Deputy Aff"
	party := "Party Aff"
	depID, _, err := storer.AddPerson(ctx, deputy)
	require.NoError(t, err)
	partyID, _, err := storer.AddPerson(ctx, party)
	require.NoError(t, err)

	// Create affiliation (valid from 2024-01-01 to open-ended)
	tx, err := storer.pool.Begin(ctx)
	require.NoError(t, err)
	vf := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, storer.FindOrCreateAffiliation(ctx, tx, depID, partyID, &vf, nil, map[string]any{"source": "actor_dataset"}))
	require.NoError(t, tx.Commit(ctx))

	// Now store a fact with PublishedAt within the affiliation window
	pub := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	input := StoreFactInput{
		NormalizedFactText: "Deputy Aff says X",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Deputy Aff said something about X",
		Topics:             []string{"topic"},
		InvolvedPeople:     []string{deputy},
		SourceURL:          "http://example.com/deputy-aff",
		PublishedAt:        &pub,
		SourceType:         "test",
	}
	factID, factSourceID, personIDs, err := storer.StoreFact(ctx, input)
	require.NoError(t, err)
	require.NotZero(t, factID)
	require.NotZero(t, factSourceID)

	// Confirm deputy appears in returned personIDs
	var deputyFound bool
	var partyFound bool
	for _, pid := range personIDs {
		if pid == depID { deputyFound = true }
		if pid == partyID { partyFound = true }
	}
	assert.True(t, deputyFound, "expected deputy ID in returned personIDs")
	assert.True(t, partyFound, "expected party ID (affiliation) in returned personIDs")

	// Finally confirm both are linked in fact_mentions table
	rows, err := storer.pool.Query(ctx, "SELECT person_id FROM fact_mentions WHERE fact_source_id = $1", factSourceID)
	require.NoError(t, err)
	defer rows.Close()
	found := map[int64]bool{}
	for rows.Next() {
		var pid int64
		require.NoError(t, rows.Scan(&pid))
		found[pid] = true
	}
	assert.True(t, found[depID], "deputy mention not found")
	assert.True(t, found[partyID], "party mention not found")
}

// TestPostgresStorer_StoreFact_DuplicateFact_DifferentSource tests that storing a fact
// with the same fact_text as an existing fact, but a different SourceURL, fails due to
// the unique constraint on facts.fact_text.
func TestPostgresStorer_StoreFact_DuplicateFact_DifferentSource(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	commonFactText := "This fact text must be unique."
	input1 := StoreFactInput{
		NormalizedFactText: commonFactText,
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Quote for source 1.",
		Topics:             []string{"duplicate_test"},
		InvolvedPeople:     []string{"Tester"},
		SourceURL:          "http://example.com/source1-unique-fact",
		PublishedAt:        nil,
	}

	// --- Action: Store the fact first time ---
	factID1, _, _, err := storer.StoreFact(ctx, input1)
	require.NoError(t, err, "First StoreFact failed")
	require.Positive(t, factID1)

	// --- Action: Store again with same fact_text, different SourceURL ---
	input2 := StoreFactInput{
		NormalizedFactText: commonFactText,  // Same fact text
		Embedding:          mockEmbedding(), // Can be different embedding
		OriginalQuote:      "Quote for source 2.",
		Topics:             []string{"another_topic"},
		InvolvedPeople:     []string{"Tester", "Another Person"},
		SourceURL:          "http://example.com/source2-different-url", // Different SourceURL
		PublishedAt:        nil,
	}

	_, _, _, err = storer.StoreFact(ctx, input2)
	require.Error(t, err, "Second StoreFact with same fact_text (different URL) should fail")

	// Check if the error is a PostgreSQL unique violation error (code 23505) on facts.fact_text
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		assert.Equal(t, "23505", pgErr.Code, "Expected PostgreSQL unique violation on facts.fact_text (23505)")
		assert.Contains(t, pgErr.ConstraintName, "facts_fact_text_key", "Error message should mention facts_fact_text_key constraint")
	} else {
		t.Logf("Warning: Could not assert specific PostgreSQL error type for fact_text unique violation. Error: %v", err)
		assert.ErrorContains(t, err, "duplicate key value violates unique constraint") // Generic check
	}

	// Verify only one fact with that text exists
	var count int64
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM facts WHERE fact_text = $1", commonFactText).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "Should still only be 1 fact entry with that text")
}

// TestPostgresStorer_StoreFact_ExistingPerson tests storing facts where persons involved might already exist.
func TestPostgresStorer_StoreFact_ExistingPerson(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	personX := "Person X"
	personY := "Person Y"
	normalizedX := util.NormalizeString(personX)
	normalizedY := util.NormalizeString(personY)

	// Input 1: Involves Person X
	input1 := StoreFactInput{
		NormalizedFactText: "Fact 1 about Person X",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Quote for fact 1",
		Topics:             []string{"topic1"},
		InvolvedPeople:     []string{personX},
		SourceURL:          "http://example.com/fact1-personx",
	}
	// Handle 4 return values
	factID1, factSourceID1, _, err := storer.StoreFact(ctx, input1)
	require.NoError(t, err, "StoreFact for input1 failed")

	// Get Person X's ID
	var personXID int64
	err = storer.pool.QueryRow(ctx, "SELECT id FROM persons WHERE name = $1", normalizedX).Scan(&personXID)
	require.NoError(t, err, "Failed to get Person X ID after fact1")
	require.Positive(t, personXID)

	// Input 2: Involves Person X (different casing) and Person Y
	input2 := StoreFactInput{
		NormalizedFactText: "Fact 2 involving Person X and Person Y",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Quote for fact 2",
		Topics:             []string{"topic2"},
		InvolvedPeople:     []string{" person X ", personY},
		SourceURL:          "http://example.com/fact2-personxy",
	}
	// Handle 4 return values
	factID2, factSourceID2, _, err := storer.StoreFact(ctx, input2)
	require.NoError(t, err, "StoreFact for input2 failed")

	// --- Verification ---
	// 1. Check Person count
	var personCount int64
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM persons").Scan(&personCount)
	require.NoError(t, err)
	assert.Equal(t, int64(2), personCount, "Should only be 2 persons (X and Y) in the table")

	// 2. Verify Person X ID hasn't changed
	var personXID_after int64
	err = storer.pool.QueryRow(ctx, "SELECT id FROM persons WHERE name = $1", normalizedX).Scan(&personXID_after)
	require.NoError(t, err)
	assert.Equal(t, personXID, personXID_after, "Person X ID should remain the same")

	// 3. Verify Person Y exists
	var personYID int64
	err = storer.pool.QueryRow(ctx, "SELECT id FROM persons WHERE name = $1", normalizedY).Scan(&personYID)
	require.NoError(t, err)
	require.Positive(t, personYID)

	// 4. Verify Mentions for Fact 1 (should only link Person X)
	var mentionCount1 int64
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_mentions WHERE fact_source_id = $1", factSourceID1).Scan(&mentionCount1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), mentionCount1, "Fact 1 source should have 1 mention")
	var mention1PersonID int64
	err = storer.pool.QueryRow(ctx, "SELECT person_id FROM fact_mentions WHERE fact_source_id = $1", factSourceID1).Scan(&mention1PersonID)
	require.NoError(t, err)
	assert.Equal(t, personXID, mention1PersonID, "Mention for Fact 1 source should be Person X")

	// 5. Verify Mentions for Fact 2 (should link Person X and Person Y)
	var mentionCount2 int64
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_mentions WHERE fact_source_id = $1", factSourceID2).Scan(&mentionCount2)
	require.NoError(t, err)
	assert.Equal(t, int64(2), mentionCount2, "Fact 2 source should have 2 mentions")
	rows, err := storer.pool.Query(ctx, "SELECT person_id FROM fact_mentions WHERE fact_source_id = $1", factSourceID2)
	require.NoError(t, err)
	foundMentions2 := make(map[int64]bool)
	for rows.Next() {
		var pid int64
		rows.Scan(&pid)
		foundMentions2[pid] = true
	}
	rows.Close()
	assert.True(t, foundMentions2[personXID], "Fact 2 mention missing for Person X")
	assert.True(t, foundMentions2[personYID], "Fact 2 mention missing for Person Y")

	// 6. Verify fact_sources are linked correctly
	var source1FactID, source2FactID string // pgx scans int8 as string
	err = storer.pool.QueryRow(ctx, "SELECT fact_id FROM fact_sources WHERE id = $1", factSourceID1).Scan(&source1FactID)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%d", factID1), source1FactID)

	err = storer.pool.QueryRow(ctx, "SELECT fact_id FROM fact_sources WHERE id = $1", factSourceID2).Scan(&source2FactID)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("%d", factID2), source2FactID)
}

// TestPostgresStorer_IsURLVisitable tests the IsURLVisitable function.
func TestPostgresStorer_IsURLVisitable(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	testURL1 := "http://example.com/visitable/new"
	testURL2 := "http://example.com/visitable/recent"
	testURL3 := "http://example.com/visitable/old"
	revisitIntervalHours := 24

	// Case 1: URL does not exist
	visitable, exists, err := storer.IsURLVisitable(ctx, testURL1, revisitIntervalHours)
	require.NoError(t, err, "IsURLVisitable failed for new URL")
	assert.True(t, visitable, "New URL should be visitable")
	assert.False(t, exists, "New URL should not exist in sources")

	// Setup for existing URLs: Insert some sources directly
	_, err = storer.pool.Exec(ctx, `
		INSERT INTO facts (fact_text, embedding, topics) VALUES 
		('dummy fact for url2', $1, '{}'),
		('dummy fact for url3', $1, '{}') RETURNING id
	`, pgvector.NewVector(mockEmbedding()))
	require.NoError(t, err, "Failed to insert dummy facts for IsURLVisitable test")

	var factIDForURL2, factIDForURL3 int64
	err = storer.pool.QueryRow(ctx, "SELECT id FROM facts WHERE fact_text = 'dummy fact for url2'").Scan(&factIDForURL2)
	require.NoError(t, err)
	err = storer.pool.QueryRow(ctx, "SELECT id FROM facts WHERE fact_text = 'dummy fact for url3'").Scan(&factIDForURL3)
	require.NoError(t, err)

	// Visited 1 hour ago (recent)
	lastVisitedRecent := time.Now().Add(-1 * time.Hour)
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_sources (fact_id, source_url, original_quote, published_at, last_visited_at) VALUES ($1, $2, $3, $4, $5)",
		factIDForURL2, testURL2, "quote2", time.Now(), lastVisitedRecent)
	require.NoError(t, err, "Failed to insert recent source for IsURLVisitable")

	// Visited 48 hours ago (old, past the 24-hour interval)
	lastVisitedOld := time.Now().Add(-48 * time.Hour)
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_sources (fact_id, source_url, original_quote, published_at, last_visited_at) VALUES ($1, $2, $3, $4, $5)",
		factIDForURL3, testURL3, "quote3", time.Now(), lastVisitedOld)
	require.NoError(t, err, "Failed to insert old source for IsURLVisitable")

	// Case 2: URL exists, visited recently
	visitable, exists, err = storer.IsURLVisitable(ctx, testURL2, revisitIntervalHours)
	require.NoError(t, err, "IsURLVisitable failed for recently visited URL")
	assert.False(t, visitable, "Recently visited URL should not be visitable")
	assert.True(t, exists, "Recently visited URL should exist in sources")

	// Case 3: URL exists, visited long ago (older than revisitIntervalHours)
	visitable, exists, err = storer.IsURLVisitable(ctx, testURL3, revisitIntervalHours)
	require.NoError(t, err, "IsURLVisitable failed for old URL")
	assert.True(t, visitable, "Old URL (past interval) should be visitable")
	assert.True(t, exists, "Old URL should exist in sources")

	// Case 4: URL exists, but revisitInterval is very short (e.g., 0 hours), so it should be visitable
	visitable, exists, err = storer.IsURLVisitable(ctx, testURL2, 0) // 0 hour interval
	require.NoError(t, err, "IsURLVisitable failed for recent URL with 0 interval")
	assert.True(t, visitable, "Recently visited URL should be visitable with 0 interval")
	assert.True(t, exists, "Recently visited URL should still exist in sources")
}

// TODO: Add test case for storing the same fact again (should ideally be idempotent or handled)
// TODO: Add test case for storing fact with existing person
