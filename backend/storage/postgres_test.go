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
		// Allow tests to run if MISTRAL key is set directly in env
		if os.Getenv("MISTRAL_API_KEY") == "" || os.Getenv("DATABASE_URL") == "" {
			t.Log("INFO: .env file not found and MISTRAL_API_KEY or DATABASE_URL not set in env, tests needing them might be skipped.")
		}
	}
}

// setupTestDB connects to the test database and returns a storer and a teardown function.
func setupTestDB(t *testing.T) (*PostgresStorer, func()) {
	loadEnv(t)
	dbURL := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, dbURL, "DATABASE_URL must be set for storage tests")
	mistralKey := os.Getenv("MISTRAL_API_KEY")
	require.NotEmpty(t, mistralKey, "MISTRAL_API_KEY must be set for storage tests") // Needed for embedder

	ctx := context.Background()

	// Create storer (no longer needs an embedder instance)
	storer, err := NewPostgresStorerFromEnv(ctx) // Removed embedder argument
	require.NoError(t, err, "Failed to create PostgresStorer")

	// Teardown function to close the pool
	teardown := func() {
		storer.Close()
	}

	// Clean up relevant tables before running the test
	// Use TRUNCATE ... RESTART IDENTITY CASCADE for clean slate
	_, err = storer.pool.Exec(ctx, "TRUNCATE TABLE fact_mentions, facts, persons RESTART IDENTITY CASCADE")
	require.NoError(t, err, "Failed to truncate tables")

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
	}

	// --- Action: Store the fact ---
	factID, factSourceID, err := storer.StoreFact(ctx, input)
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
	var queriedFactID, queriedSourceURL, queriedOriginalQuote string
	var queriedPublishedAt *time.Time
	sourceQuery := "SELECT fact_id, source_url, original_quote, published_at FROM fact_sources WHERE id = $1"
	err = storer.pool.QueryRow(ctx, sourceQuery, factSourceID).Scan(
		&queriedFactID, &queriedSourceURL, &queriedOriginalQuote, &queriedPublishedAt,
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

// TestPostgresStorer_StoreFact_Idempotency_SameSource verifies storing the same fact
// from the same source URL twice results in one fact, one source (possibly updated), and correct mentions.
func TestPostgresStorer_StoreFact_Idempotency_SameSource(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	input1 := StoreFactInput{
		NormalizedFactText: "Idempotency test fact.",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Original quote for idempotency.",
		Topics:             []string{"idempotency"},
		InvolvedPeople:     []string{"Idempotent Person"},
		SourceURL:          "http://example.com/idempotent",
		PublishedAt:        nil, // Optional
	}

	// --- Action: Store the fact first time ---
	factID1, factSourceID1, err := storer.StoreFact(ctx, input1)
	require.NoError(t, err, "First StoreFact failed")
	require.Positive(t, factID1)
	require.Positive(t, factSourceID1)

	// --- Action: Store again with slightly different quote, same core info ---
	input2 := input1 // Copy struct
	input2.OriginalQuote = "Updated quote for idempotency."
	timeNow := time.Now()
	input2.PublishedAt = &timeNow // Update published date

	factID2, factSourceID2, err := storer.StoreFact(ctx, input2)
	require.NoError(t, err, "Second StoreFact failed")

	// --- Verification ---
	assert.Equal(t, factID1, factID2, "Fact ID should be the same for both calls")
	assert.Equal(t, factSourceID1, factSourceID2, "Fact Source ID should be the same (updated row)")

	// Check counts in DB
	var count int64
	// 1. Facts count
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM facts WHERE fact_text = $1", input1.NormalizedFactText).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "Should only be 1 fact entry")

	// 2. Persons count
	normalizedName := util.NormalizeString(input1.InvolvedPeople[0])
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM persons WHERE name = $1", normalizedName).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "Should only be 1 person entry")

	// 3. Fact Sources count
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_sources WHERE fact_id = $1 AND source_url = $2", factID1, input1.SourceURL).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "Should only be 1 fact_source entry for this fact/URL combo")

	// 4. Verify updated fields in fact_sources
	var updatedQuote string
	var updatedPublishedAt *time.Time
	err = storer.pool.QueryRow(ctx, "SELECT original_quote, published_at FROM fact_sources WHERE id = $1", factSourceID1).Scan(&updatedQuote, &updatedPublishedAt)
	require.NoError(t, err)
	assert.Equal(t, input2.OriginalQuote, updatedQuote, "Original quote should be updated")
	require.NotNil(t, updatedPublishedAt)
	assert.WithinDuration(t, *input2.PublishedAt, *updatedPublishedAt, time.Second, "PublishedAt should be updated")

	// 5. Fact Mentions count
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_mentions WHERE fact_source_id = $1", factSourceID1).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(len(input1.InvolvedPeople)), count, "Should only be mentions linked to the single fact_source entry")
}

// TestPostgresStorer_StoreFact_DuplicateFact_DifferentSource tests storing the same logical fact
// from two different source URLs.
func TestPostgresStorer_StoreFact_DuplicateFact_DifferentSource(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	factText := "Duplicate fact, different sources."
	embedding := mockEmbedding() // Same embedding represents same semantic meaning
	person := "Duplicate Person"

	input1 := StoreFactInput{
		NormalizedFactText: factText,
		Embedding:          embedding,
		OriginalQuote:      "Quote from source 1.",
		Topics:             []string{"duplicates"},
		InvolvedPeople:     []string{person},
		SourceURL:          "http://example.com/duplicate-1",
	}
	input2 := StoreFactInput{
		NormalizedFactText: factText,  // Same fact text
		Embedding:          embedding, // Same embedding
		OriginalQuote:      "Quote from source 2.",
		Topics:             []string{"duplicates", "source2"}, // Can have different topics (fact ones should be updated)
		InvolvedPeople:     []string{person},                  // Same person
		SourceURL:          "http://example.com/duplicate-2",  // Different URL
	}

	// --- Action: Store both ---
	factID1, factSourceID1, err := storer.StoreFact(ctx, input1)
	require.NoError(t, err, "StoreFact for input1 failed")

	factID2, factSourceID2, err := storer.StoreFact(ctx, input2)
	require.NoError(t, err, "StoreFact for input2 failed")

	// --- Verification ---
	assert.Equal(t, factID1, factID2, "Fact ID should be the same (duplicate fact)")
	assert.NotEqual(t, factSourceID1, factSourceID2, "Fact Source ID should be different (different sources)")

	// Check counts
	var count int64
	// 1. Facts count
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM facts WHERE fact_text = $1", factText).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "Should only be 1 fact entry")

	// 2. Persons count
	normalizedName := util.NormalizeString(person)
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM persons WHERE name = $1", normalizedName).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(1), count, "Should only be 1 person entry")

	// 3. Fact Sources count
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_sources WHERE fact_id = $1", factID1).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, int64(2), count, "Should be 2 fact_source entries for this fact")

	// 4. Fact Mentions count
	// Should have one mention for source 1 and one mention for source 2
	var mentionCount1, mentionCount2 int64
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_mentions WHERE fact_source_id = $1", factSourceID1).Scan(&mentionCount1)
	require.NoError(t, err)
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_mentions WHERE fact_source_id = $1", factSourceID2).Scan(&mentionCount2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), mentionCount1, "Should be 1 mention for source 1")
	assert.Equal(t, int64(1), mentionCount2, "Should be 1 mention for source 2")
}

// TestPostgresStorer_StoreFact_ExistingPerson tests storing facts involving the same person.
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
	factID1, factSourceID1, err := storer.StoreFact(ctx, input1)
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
	factID2, factSourceID2, err := storer.StoreFact(ctx, input2)
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
	var source1FactID, source2FactID int64
	err = storer.pool.QueryRow(ctx, "SELECT fact_id FROM fact_sources WHERE id = $1", factSourceID1).Scan(&source1FactID)
	require.NoError(t, err)
	assert.Equal(t, factID1, source1FactID, "Fact source 1 not linked to fact 1")
	err = storer.pool.QueryRow(ctx, "SELECT fact_id FROM fact_sources WHERE id = $1", factSourceID2).Scan(&source2FactID)
	require.NoError(t, err)
	assert.Equal(t, factID2, source2FactID, "Fact source 2 not linked to fact 2")
}

// TestPostgresStorer_CheckSourceURLExists tests the helper function for checking URLs.
func TestPostgresStorer_CheckSourceURLExists(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	urlToCheck1 := "http://example.com/non-existent-url"
	urlToCheck2 := "http://example.com/existent-url"

	// 1. Check non-existent URL
	exists, err := storer.CheckSourceURLExists(ctx, urlToCheck1)
	require.NoError(t, err, "CheckSourceURLExists failed for non-existent URL")
	assert.False(t, exists, "CheckSourceURLExists should return false for non-existent URL")

	// 2. Insert a fact with urlToCheck2
	input := StoreFactInput{
		NormalizedFactText: "Fact for URL check",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Quote for URL check",
		InvolvedPeople:     []string{"URL Checker"},
		SourceURL:          urlToCheck2,
	}
	_, _, err = storer.StoreFact(ctx, input)
	require.NoError(t, err, "StoreFact failed during URL check test setup")

	// 3. Check existent URL
	exists, err = storer.CheckSourceURLExists(ctx, urlToCheck2)
	require.NoError(t, err, "CheckSourceURLExists failed for existent URL")
	assert.True(t, exists, "CheckSourceURLExists should return true for existent URL")

	// 4. Check non-existent URL again (sanity check)
	exists, err = storer.CheckSourceURLExists(ctx, urlToCheck1)
	require.NoError(t, err, "CheckSourceURLExists failed for non-existent URL (second check)")
	assert.False(t, exists, "CheckSourceURLExists should return false for non-existent URL (second check)")
}

// TODO: Add test case for storing the same fact again (should ideally be idempotent or handled)
// TODO: Add test case for storing fact with existing person
