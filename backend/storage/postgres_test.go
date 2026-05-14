package storage

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/util"
	"fmt"
	"math/rand"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/pgvector/pgvector-go"    // Import pgvector
	"github.com/stretchr/testify/assert" // Using testify for clearer assertions
	"github.com/stretchr/testify/require"
)

// embeddingDimension defines the dimension for mock embeddings.
// Must match the current pgvector column dimension (see
// supabase/migrations/20260510000000_resize_embedding_to_4096.sql) and the
// targetEmbeddingDim constant in backend/embedding/openai_embeddings.go.
const embeddingDimension = 4096

// Helper function to load .env for tests
func loadEnv(t *testing.T) {
	t.Helper()
	err := godotenv.Overload("../../.env")
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
	if dbURL == "" {
		t.Skip("DATABASE_URL not set, skipping storage tests")
	}

	ctx := context.Background()

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
	_, err = storer.pool.Exec(ctx, "TRUNCATE TABLE fact_mentions, facts, fact_sources, person_links, persons, contradictions, processed_laws RESTART IDENTITY CASCADE")
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

// stubStanceProcessor is a minimal StanceProcessor for tests: every fact gets
// its own stance row so the "every fact has a stance" invariant holds without
// invoking the real LLM-based clustering.
type stubStanceProcessor struct {
	storer *PostgresStorer
}

func (s *stubStanceProcessor) ProcessNewFact(ctx context.Context, personID int64, factID int64, factText string, embedding []float32, _ string) (*core.StanceProcessResult, error) {
	label := factText
	if len(label) > 120 {
		label = label[:120]
	}
	stanceID, err := s.storer.InsertStanceDocument(ctx, StanceDocument{
		PersonID:         personID,
		ClusterLabel:     label,
		ClusterEmbedding: embedding,
		Summary:          factText,
		KeyFactIDs:       []int64{factID},
		FactCount:        1,
	})
	if err != nil {
		return nil, fmt.Errorf("stub stance: insert stance: %w", err)
	}
	if err := s.storer.AddFactToStance(ctx, stanceID, factID); err != nil {
		return nil, fmt.Errorf("stub stance: link fact: %w", err)
	}
	return &core.StanceProcessResult{Verdict: "NEW_STANCE", StanceID: stanceID}, nil
}

// Helper function to insert a fact using core.FactInserter (replaces old StoreFact for tests)
// Returns the canonical fact ID and fact_source ID that was inserted
func insertFactForTest(ctx context.Context, t *testing.T, storer *PostgresStorer, input StoreFactInput) (factID int64, factSourceID int64, err error) {
	t.Helper()

	// Convert to core.StoreFactInput
	coreInput := core.StoreFactInput{
		SubjectEntity:      input.SubjectEntity,
		NormalizedFactText: input.NormalizedFactText,
		Embedding:          input.Embedding,
		OriginalQuote:      input.OriginalQuote,
		CanonicalVerb:      input.CanonicalVerb,
		InvolvedPeople:     input.InvolvedPeople,
		SourceURL:          input.SourceURL,
		PublishedAt:        input.PublishedAt,
		SourceType:         input.SourceType,
		Metadata:           input.Metadata,
	}

	// Use core.FactInserter to insert (with a stub stance processor for tests)
	inserter := core.NewFactInserter(storer, &stubStanceProcessor{storer: storer})
	_, err = inserter.InsertFacts(ctx, []core.StoreFactInput{coreInput})
	if err != nil {
		return 0, 0, err
	}

	// Query to get the fact ID and source ID that were just inserted
	// We look for the most recently inserted fact with this text
	query := `
		SELECT f.id, fs.id
		FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		WHERE f.fact_text = $1 AND fs.source_url = $2
		ORDER BY f.id DESC, fs.id DESC
		LIMIT 1`
	err = storer.pool.QueryRow(ctx, query, input.NormalizedFactText, input.SourceURL).Scan(&factID, &factSourceID)
	return factID, factSourceID, err
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
		SubjectEntity:      "Person A",
		NormalizedFactText: "Person A and Person B agree on Policy X.",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "In a joint statement, Person A and B declared their full support for Policy X.",
		CanonicalVerb:      "SUPPORTED",
		InvolvedPeople:     []string{"Person B"}, // Secondary person mentioned
		SourceURL:          "http://example.com/policy-agreement-1",
		PublishedAt:        &pubDate,
		SourceType:         "test",
		Metadata:           map[string]any{"k": "v"},
	}

	// --- Action: Store the fact ---
	// Handle 4 return values, ignore personIDs for this test
	factID, factSourceID, err := insertFactForTest(ctx, t, storer, input)
	require.NoError(t, err, "StoreFact failed")
	require.Positive(t, factID, "StoreFact should return a positive factID")
	require.Positive(t, factSourceID, "StoreFact should return a positive factSourceID")

	// --- Verification ---
	var queriedFactText string
	var queriedEmbedding pgvector.Vector
	// 1. Verify the fact exists in 'facts' table
	factQuery := "SELECT fact_text, embedding, COALESCE(canonical_verb, '') FROM facts WHERE id = $1"
	var queriedCanonicalVerb string
	err = storer.pool.QueryRow(ctx, factQuery, factID).Scan(
		&queriedFactText, &queriedEmbedding, &queriedCanonicalVerb,
	)
	require.NoError(t, err, "Failed to query inserted fact by ID")
	assert.Equal(t, input.NormalizedFactText, queriedFactText)
	assert.Equal(t, input.CanonicalVerb, queriedCanonicalVerb)
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

	// 3. Verify only the subject entity person exists (InvolvedPeople no longer create DB links)
	var personAID int64
	err = storer.pool.QueryRow(ctx, "SELECT id FROM persons WHERE name = $1", "person a").Scan(&personAID)
	require.NoError(t, err, "Subject entity 'person a' should exist")
	require.Positive(t, personAID)

	// Person B (InvolvedPeople only) should NOT be in the DB
	var personBCount int
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM persons WHERE name = $1", "person b").Scan(&personBCount)
	require.NoError(t, err)
	assert.Equal(t, 0, personBCount, "InvolvedPeople should not create person records")

	// 4. Verify the fact_mentions link exists only for subject entity
	mentionsQuery := "SELECT person_id FROM fact_mentions WHERE fact_source_id = $1"
	mentionRows, err := storer.pool.Query(ctx, mentionsQuery, factSourceID)
	require.NoError(t, err, "Failed to query fact_mentions")
	mentionCount := 0
	for mentionRows.Next() {
		var personID int64
		err = mentionRows.Scan(&personID)
		require.NoError(t, err, "Failed to scan mention row")
		assert.Equal(t, personAID, personID, "Mention should be for subject entity Person A")
		mentionCount++
	}
	require.NoError(t, mentionRows.Err(), "Error iterating mention rows")
	assert.Equal(t, 1, mentionCount, "Should have exactly 1 mention (subject entity only)")
	mentionRows.Close()
}
// DISABLED: 
// DISABLED: // TestPostgresStorer_StoreFact_Idempotency_And_SourceUpdate tests:
// DISABLED: // 1. Storing the exact same fact (same text) twice fails due to unique constraint on facts.fact_text.
// DISABLED: // 2. Storing a *different* fact from an *existing source URL* creates a new fact_sources record.
// DISABLED: //    (As of migration 20251227000000, the URL unique constraint was removed to allow multiple facts per URL)
// DISABLED: func TestPostgresStorer_StoreFact_Idempotency_And_SourceUpdate(t *testing.T) {
// DISABLED: 	storer, teardown := setupTestDB(t)
// DISABLED: 	defer teardown()
// DISABLED: 	ctx := context.Background()
// DISABLED: 
// DISABLED: 	commonSourceURL := "http://example.com/idempotent-and-update"
// DISABLED: 	pubDate1 := time.Now().Add(-2 * time.Hour)
// DISABLED: 
// DISABLED: 	input1 := StoreFactInput{
// DISABLED: 		NormalizedFactText: "Idempotency test fact one.",
// DISABLED: 		Embedding:          mockEmbedding(),
// DISABLED: 		OriginalQuote:      "Original quote for fact one.",
// DISABLED: 		Topics:             []string{"idempotency", "update"},
// DISABLED: 		InvolvedPeople:     []string{"Updater Person"},
// DISABLED: 		SourceURL:          commonSourceURL,
// DISABLED: 		PublishedAt:        &pubDate1,
// DISABLED: 	}
// DISABLED: 
// DISABLED: 	// --- Action: Store the fact first time ---
// DISABLED: 	factID1, factSourceID1, _, err := storer.StoreFact(ctx, input1)
// DISABLED: 	require.NoError(t, err, "First StoreFact failed")
// DISABLED: 	require.Positive(t, factID1)
// DISABLED: 	require.Positive(t, factSourceID1)
// DISABLED: 
// DISABLED: 	// --- Part 1: Test Idempotency for exact same fact text ---
// DISABLED: 	input1Attempt2 := input1 // Exact same input
// DISABLED: 	input1Attempt2.OriginalQuote = "Slightly different quote, but same fact text."
// DISABLED: 	_, _, err = insertFactForTest(ctx, t, storer, input1Attempt2)
// DISABLED: 	require.Error(t, err, "Second StoreFact with exact same fact_text should fail")
// DISABLED: 	var pgErr *pgconn.PgError
// DISABLED: 	if errors.As(err, &pgErr) {
// DISABLED: 		assert.Equal(t, "23505", pgErr.Code, "Expected PostgreSQL unique violation on facts.fact_text (23505)")
// DISABLED: 		assert.Contains(t, pgErr.ConstraintName, "facts_fact_text_key", "Error message should mention facts_fact_text_key constraint")
// DISABLED: 	} else {
// DISABLED: 		t.Logf("Warning: Could not assert specific PostgreSQL error type for fact_text unique violation. Error: %v", err)
// DISABLED: 		assert.ErrorContains(t, err, "duplicate key value violates unique constraint") // Generic check
// DISABLED: 	}
// DISABLED:
// DISABLED: 	// --- Part 2: Test storing a *different* fact from the *same source URL* ---
// DISABLED: 	// A new fact_sources record should be created (URL unique constraint was removed)
// DISABLED: 	pubDate2 := time.Now().Add(-1 * time.Hour)
// DISABLED: 	input2 := StoreFactInput{
// DISABLED: 		NormalizedFactText: "A completely different fact two.", // Different fact text
// DISABLED: 		Embedding:          mockEmbedding(),                    // Different embedding
// DISABLED: 		OriginalQuote:      "Quote for the second, different fact.",
// DISABLED: 		Topics:             []string{"new topic"},
// DISABLED: 		InvolvedPeople:     []string{"Updater Person", "New Person"}, // Can have different people
// DISABLED: 		SourceURL:          commonSourceURL,                          // Same source URL as input1
// DISABLED: 		PublishedAt:        &pubDate2,
// DISABLED: 	}
// DISABLED:
// DISABLED: 	factID1, factSourceID1, err := insertFactForTest(ctx, t, storer, input2)
// DISABLED: 	require.NoError(t, err, "insertFactForTest for a different fact from same URL should succeed")
// DISABLED: 	require.Positive(t, factID2, "factID2 should be positive")
// DISABLED: 	require.NotEqual(t, factID1, factID2, "factID1 and factID2 should be different")
// DISABLED:
// DISABLED: 	// Critical: A new fact_sources record should be created (different IDs)
// DISABLED: 	assert.NotEqual(t, factSourceID1, factSourceID2, "fact_source_id should be different (new record created)")
// DISABLED:
// DISABLED: 	// Verify there are now TWO fact_sources records with this URL
// DISABLED: 	var sourceCount int
// DISABLED: 	countQuery := "SELECT COUNT(*) FROM fact_sources WHERE source_url = $1"
// DISABLED: 	err = storer.pool.QueryRow(ctx, countQuery, commonSourceURL).Scan(&sourceCount)
// DISABLED: 	require.NoError(t, err, "Failed to count fact_sources records")
// DISABLED: 	assert.Equal(t, 2, sourceCount, "Should have 2 fact_sources records for the same URL")
// DISABLED:
// DISABLED: 	// Verify both facts still exist
// DISABLED: 	var fact1Exists, fact2Exists bool
// DISABLED: 	err = storer.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM facts WHERE id = $1)", factID1).Scan(&fact1Exists)
// DISABLED: 	require.NoError(t, err)
// DISABLED: 	assert.True(t, fact1Exists, "The first fact (factID1) should exist in the facts table")
// DISABLED:
// DISABLED: 	err = storer.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM facts WHERE id = $1)", factID2).Scan(&fact2Exists)
// DISABLED: 	require.NoError(t, err)
// DISABLED: 	assert.True(t, fact2Exists, "The second fact (factID2) should exist in the facts table")
// DISABLED: }

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

// TestStoreFact_CrossAttachAffiliations tests that affiliations can be created and queried,
// but StoreFact does NOT automatically link affiliated parties (as of the intentional removal
// noted in postgres.go:308-312). Plugins must explicitly create party-level facts when needed.
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

	// Verify affiliation was created
	tx2, err := storer.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx2.Rollback(ctx)

	pub := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	affiliatedIDs, err := storer.GetAffiliatedPersonIDs(ctx, tx2, depID, &pub)
	require.NoError(t, err)
	assert.Equal(t, 1, len(affiliatedIDs), "Deputy should have 1 affiliation")
	assert.Equal(t, partyID, affiliatedIDs[0], "Deputy should be affiliated with party")

	// Now store a fact mentioning only the deputy
	input := StoreFactInput{
		SubjectEntity:      deputy, // The fact is about the deputy
		NormalizedFactText: "Deputy Aff says X",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Deputy Aff said something about X",
		CanonicalVerb:      "",
		SourceURL:          "http://example.com/deputy-aff",
		PublishedAt:        &pub,
		SourceType:         "test",
	}
	factID, factSourceID, err := insertFactForTest(ctx, t, storer, input)
	require.NoError(t, err)
	require.NotZero(t, factID)
	require.NotZero(t, factSourceID)

	// Verify only deputy is linked in fact_mentions table (not the party)
	rows, err := storer.pool.Query(ctx, "SELECT person_id FROM fact_mentions WHERE fact_source_id = $1", factSourceID)
	require.NoError(t, err)
	defer rows.Close()
	found := map[int64]bool{}
	for rows.Next() {
		var pid int64
		require.NoError(t, rows.Scan(&pid))
		found[pid] = true
	}
	assert.True(t, found[depID], "deputy mention should be found")
	assert.False(t, found[partyID], "party mention should NOT be found (no automatic cross-attachment)")
}
// DISABLED: 
// DISABLED: // TestPostgresStorer_StoreFact_DuplicateFact_DifferentSource tests that storing a fact
// DISABLED: // with the same fact_text as an existing fact, but a different SourceURL, fails due to
// DISABLED: // the unique constraint on facts.fact_text.
// DISABLED: func TestPostgresStorer_StoreFact_DuplicateFact_DifferentSource(t *testing.T) {
// DISABLED: 	storer, teardown := setupTestDB(t)
// DISABLED: 	defer teardown()
// DISABLED: 	ctx := context.Background()
// DISABLED: 
// DISABLED: 	commonFactText := "This fact text must be unique."
// DISABLED: 	input1 := StoreFactInput{
// DISABLED: 		NormalizedFactText: commonFactText,
// DISABLED: 		Embedding:          mockEmbedding(),
// DISABLED: 		OriginalQuote:      "Quote for source 1.",
// DISABLED: 		Topics:             []string{"duplicate_test"},
// DISABLED: 		InvolvedPeople:     []string{"Tester"},
// DISABLED: 		SourceURL:          "http://example.com/source1-unique-fact",
// DISABLED: 		PublishedAt:        nil,
// DISABLED: 	}
// DISABLED: 
// DISABLED: 	// --- Action: Store the fact first time ---
// DISABLED: 	factID1, factSourceID1, err := insertFactForTest(ctx, t, storer, input1)
// DISABLED: 	require.NoError(t, err, "First insertFactForTest failed")
// DISABLED: 	require.Positive(t, factID1)
// DISABLED: 
// DISABLED: 	// --- Action: Store again with same fact_text, different SourceURL ---
// DISABLED: 	input2 := StoreFactInput{
// DISABLED: 		NormalizedFactText: commonFactText,  // Same fact text
// DISABLED: 		Embedding:          mockEmbedding(), // Can be different embedding
// DISABLED: 		OriginalQuote:      "Quote for source 2.",
// DISABLED: 		Topics:             []string{"another_topic"},
// DISABLED: 		InvolvedPeople:     []string{"Tester", "Another Person"},
// DISABLED: 		SourceURL:          "http://example.com/source2-different-url", // Different SourceURL
// DISABLED: 		PublishedAt:        nil,
// DISABLED: 	}
// DISABLED: 
// DISABLED: 	_, _, err = insertFactForTest(ctx, t, storer, input2)
// DISABLED: 	require.Error(t, err, "Second StoreFact with same fact_text (different URL) should fail")
// DISABLED:
// DISABLED: 	// Check if the error is a PostgreSQL unique violation error (code 23505) on facts.fact_text
// DISABLED: 	var pgErr *pgconn.PgError
// DISABLED: 	if errors.As(err, &pgErr) {
// DISABLED: 		assert.Equal(t, "23505", pgErr.Code, "Expected PostgreSQL unique violation on facts.fact_text (23505)")
// DISABLED: 		assert.Contains(t, pgErr.ConstraintName, "facts_fact_text_key", "Error message should mention facts_fact_text_key constraint")
// DISABLED: 	} else {
// DISABLED: 		t.Logf("Warning: Could not assert specific PostgreSQL error type for fact_text unique violation. Error: %v", err)
// DISABLED: 		assert.ErrorContains(t, err, "duplicate key value violates unique constraint") // Generic check
// DISABLED: 	}
// DISABLED:
// DISABLED: 	// Verify only one fact with that text exists
// DISABLED: 	var count int64
// DISABLED: 	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM facts WHERE fact_text = $1", commonFactText).Scan(&count)
// DISABLED: 	require.NoError(t, err)
// DISABLED: 	assert.Equal(t, int64(1), count, "Should still only be 1 fact entry with that text")
// DISABLED: }

// TestPostgresStorer_StoreFact_ExistingPerson tests storing facts where persons involved might already exist.
func TestPostgresStorer_StoreFact_ExistingPerson(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	personX := "Person X"
	personY := "Person Y"
	normalizedX := util.NormalizeString(personX)
	normalizedY := util.NormalizeString(personY)

	// Input 1: About Person X
	input1 := StoreFactInput{
		SubjectEntity:      personX,
		NormalizedFactText: "Fact 1 about Person X",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Quote for fact 1",
		CanonicalVerb:      "",
		SourceURL:          "http://example.com/fact1-personx",
	}
	// Handle 4 return values
	factID1, factSourceID1, err := insertFactForTest(ctx, t, storer, input1)
	require.NoError(t, err, "insertFactForTest for input1 failed")

	// Get Person X's ID
	var personXID int64
	err = storer.pool.QueryRow(ctx, "SELECT id FROM persons WHERE name = $1", normalizedX).Scan(&personXID)
	require.NoError(t, err, "Failed to get Person X ID after fact1")
	require.Positive(t, personXID)

	// Input 2: About Person X, also mentions Person Y
	input2 := StoreFactInput{
		SubjectEntity:      " person X ", // Test normalization with different casing
		NormalizedFactText: "Fact 2 involving Person X and Person Y",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Quote for fact 2",
		CanonicalVerb:      "",
		InvolvedPeople:     []string{personY}, // Secondary mention
		SourceURL:          "http://example.com/fact2-personxy",
	}
	// Handle 4 return values
	factID2, factSourceID2, err := insertFactForTest(ctx, t, storer, input2)
	require.NoError(t, err, "insertFactForTest for input2 failed")

	// --- Verification ---
	// 1. Check Person count — only 1 person (X), since InvolvedPeople no longer create DB records
	var personCount int64
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM persons").Scan(&personCount)
	require.NoError(t, err)
	assert.Equal(t, int64(1), personCount, "Should only be 1 person (X) — InvolvedPeople don't create records")

	// 2. Verify Person X ID hasn't changed
	var personXID_after int64
	err = storer.pool.QueryRow(ctx, "SELECT id FROM persons WHERE name = $1", normalizedX).Scan(&personXID_after)
	require.NoError(t, err)
	assert.Equal(t, personXID, personXID_after, "Person X ID should remain the same")

	// 3. Verify Person Y does NOT exist (InvolvedPeople no longer create DB records)
	var personYCount int
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM persons WHERE name = $1", normalizedY).Scan(&personYCount)
	require.NoError(t, err)
	assert.Equal(t, 0, personYCount, "Person Y should not exist — InvolvedPeople don't create records")

	// 4. Verify Mentions for Fact 1 (should only link Person X)
	var mentionCount1 int64
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_mentions WHERE fact_source_id = $1", factSourceID1).Scan(&mentionCount1)
	require.NoError(t, err)
	assert.Equal(t, int64(1), mentionCount1, "Fact 1 source should have 1 mention")
	var mention1PersonID int64
	err = storer.pool.QueryRow(ctx, "SELECT person_id FROM fact_mentions WHERE fact_source_id = $1", factSourceID1).Scan(&mention1PersonID)
	require.NoError(t, err)
	assert.Equal(t, personXID, mention1PersonID, "Mention for Fact 1 source should be Person X")

	// 5. Verify Mentions for Fact 2 (should link Person X only — InvolvedPeople no longer create mentions)
	var mentionCount2 int64
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_mentions WHERE fact_source_id = $1", factSourceID2).Scan(&mentionCount2)
	require.NoError(t, err)
	assert.Equal(t, int64(1), mentionCount2, "Fact 2 source should have 1 mention (subject entity only)")
	var mention2PersonID int64
	err = storer.pool.QueryRow(ctx, "SELECT person_id FROM fact_mentions WHERE fact_source_id = $1", factSourceID2).Scan(&mention2PersonID)
	require.NoError(t, err)
	assert.Equal(t, personXID, mention2PersonID, "Mention for Fact 2 source should be Person X (subject entity)")

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
		INSERT INTO facts (fact_text, embedding) VALUES
		('dummy fact for url2', $1),
		('dummy fact for url3', $1) RETURNING id
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

// TestPostgresStorer_MultipleFacts_SameLawKey tests that multiple facts can be stored
// with the same law_key in metadata (a single law generates many facts)
func TestPostgresStorer_MultipleFacts_SameLawKey(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	lawKey1 := "16-1234"
	lawKey2 := "16-5678"

	// Case 1: Store a fact with law_key in metadata
	pubDate1 := time.Now()
	input1 := StoreFactInput{
		SubjectEntity:      "Party A",
		NormalizedFactText: "Party A voted FOR law 16-1234",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Party A voted FOR...",
		CanonicalVerb:      "VOTED_FOR",
		SourceURL:          fmt.Sprintf("https://example.com/law-%s", lawKey1),
		PublishedAt:        &pubDate1,
		SourceType:         "assemblee_law_page",
		Metadata: map[string]any{
			"law_key": lawKey1,
		},
	}
	_, _, err := insertFactForTest(ctx, t, storer, input1)
	require.NoError(t, err, "StoreFact should succeed for first law_key")

	// Case 2: Store another fact with the same law_key (should succeed - multiple facts per law)
	// A single law generates multiple facts (party facts, deputy facts), all with the same law_key
	pubDate2 := time.Now()
	input2 := StoreFactInput{
		SubjectEntity:      "Party B",
		NormalizedFactText: "Party B voted AGAINST law 16-1234", // Different fact text
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Party B voted AGAINST...",
		CanonicalVerb:      "VOTED_FOR",
		SourceURL:          "https://example.com/another-source", // Different URL
		PublishedAt:        &pubDate2,
		SourceType:         "assemblee_law_page",
		Metadata: map[string]any{
			"law_key": lawKey1, // Same law_key - should succeed (multiple facts per law)
		},
	}
	_, _, err = insertFactForTest(ctx, t, storer, input2)
	require.NoError(t, err, "StoreFact should succeed for multiple facts with same law_key")

	// Verify both facts exist with the same law_key
	var countWithLawKey int
	countQuery := `SELECT COUNT(*) FROM fact_sources WHERE metadata->>'law_key' = $1`
	err = storer.pool.QueryRow(ctx, countQuery, lawKey1).Scan(&countWithLawKey)
	require.NoError(t, err)
	assert.Equal(t, 2, countWithLawKey, "Should have 2 fact_sources with the same law_key")

	// Case 3: Verify we can store facts for a different law_key
	pubDate3 := time.Now()
	input3 := StoreFactInput{
		SubjectEntity:      "Party C",
		NormalizedFactText: "Party C voted FOR law 16-5678",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Party C voted FOR...",
		CanonicalVerb:      "VOTED_FOR",
		SourceURL:          fmt.Sprintf("https://example.com/law-%s", lawKey2),
		PublishedAt:        &pubDate3,
		SourceType:         "assemblee_law_page",
		Metadata: map[string]any{
			"law_key": lawKey2,
		},
	}
	_, _, err = insertFactForTest(ctx, t, storer, input3)
	require.NoError(t, err, "StoreFact should succeed for different law_key")

	// Verify both law keys now have fact_sources records
	var countLaw1, countLaw2 int
	err = storer.pool.QueryRow(ctx, countQuery, lawKey1).Scan(&countLaw1)
	require.NoError(t, err)
	assert.Equal(t, 2, countLaw1, "Law key 1 should have 2 fact_sources")

	err = storer.pool.QueryRow(ctx, countQuery, lawKey2).Scan(&countLaw2)
	require.NoError(t, err)
	assert.Equal(t, 1, countLaw2, "Law key 2 should have 1 fact_source")
}

// TestPostgresStorer_ProcessedLawsTracking tests the processed_laws table functionality
func TestPostgresStorer_ProcessedLawsTracking(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	lawKey1 := "16-1234"
	lawKey2 := "16-5678"

	// Case 1: Initially not processed
	processed, err := storer.IsLawFullyProcessed(ctx, lawKey1)
	require.NoError(t, err, "IsLawFullyProcessed should not error for non-existent law")
	assert.False(t, processed, "Law should not be processed initially")

	// Case 2: Mark law as processed
	err = storer.MarkLawAsProcessed(ctx, lawKey1, 16, "Test Law 1234", 42)
	require.NoError(t, err, "MarkLawAsProcessed should succeed")

	// Case 3: Now should be processed
	processed, err = storer.IsLawFullyProcessed(ctx, lawKey1)
	require.NoError(t, err, "IsLawFullyProcessed should not error after marking")
	assert.True(t, processed, "Law should be marked as processed")

	// Case 4: Get processed law info
	info, err := storer.GetProcessedLawInfo(ctx, lawKey1)
	require.NoError(t, err, "GetProcessedLawInfo should not error")
	require.NotNil(t, info, "GetProcessedLawInfo should return non-nil info")
	assert.Equal(t, lawKey1, info.LawKey, "LawKey should match")
	assert.Equal(t, 16, info.Legislature, "Legislature should match")
	assert.Equal(t, "Test Law 1234", info.Title, "Title should match")
	assert.Equal(t, 42, info.FactsCount, "FactsCount should match")
	assert.False(t, info.ProcessedAt.IsZero(), "ProcessedAt should be set")

	// Case 5: Different law key should not be processed
	processed, err = storer.IsLawFullyProcessed(ctx, lawKey2)
	require.NoError(t, err, "IsLawFullyProcessed should not error for different law")
	assert.False(t, processed, "Different law should not be processed")

	// Case 6: GetProcessedLawInfo for non-existent law should return nil
	info, err = storer.GetProcessedLawInfo(ctx, lawKey2)
	require.NoError(t, err, "GetProcessedLawInfo should not error for non-existent law")
	assert.Nil(t, info, "GetProcessedLawInfo should return nil for non-existent law")

	// Case 7: Mark second law as processed
	err = storer.MarkLawAsProcessed(ctx, lawKey2, 16, "Test Law 5678", 30)
	require.NoError(t, err, "MarkLawAsProcessed should succeed for second law")

	// Both should now be processed
	processed, err = storer.IsLawFullyProcessed(ctx, lawKey1)
	require.NoError(t, err)
	assert.True(t, processed, "First law should still be processed")

	processed, err = storer.IsLawFullyProcessed(ctx, lawKey2)
	require.NoError(t, err)
	assert.True(t, processed, "Second law should be processed")

	// Case 8: Test upsert behavior (marking same law again updates timestamp)
	time.Sleep(10 * time.Millisecond) // Small delay to ensure timestamp changes
	err = storer.MarkLawAsProcessed(ctx, lawKey1, 16, "Test Law 1234", 43) // Different facts_count
	require.NoError(t, err, "MarkLawAsProcessed should succeed on upsert")

	info, err = storer.GetProcessedLawInfo(ctx, lawKey1)
	require.NoError(t, err)
	require.NotNil(t, info)
	assert.Equal(t, 43, info.FactsCount, "FactsCount should be updated to new value")
}

// TestPostgresStorer_DeletePartialLawData tests cleanup of partial law data
func TestPostgresStorer_DeletePartialLawData(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	lawKey1 := "16-9001"
	lawKey2 := "16-9002"

	// Case 1: Create partial facts for law 16-9001 (NOT in processed_laws)
	pubDate := time.Now()
	input1 := StoreFactInput{
		SubjectEntity:      "Test Person",
		NormalizedFactText: "Partial fact 1 for law 16-9001",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Quote 1",
		CanonicalVerb:      "",
		SourceURL:          "https://test.com/9001-1",
		PublishedAt:        &pubDate,
		SourceType:         "assemblee_law_page",
		Metadata: map[string]any{
			"law_key": lawKey1,
		},
	}
	factID1, factSourceID1, err := insertFactForTest(ctx, t, storer, input1)
	require.NoError(t, err)

	input2 := StoreFactInput{
		SubjectEntity:      "Test Person",
		NormalizedFactText: "Partial fact 2 for law 16-9001",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Quote 2",
		CanonicalVerb:      "",
		SourceURL:          "https://test.com/9001-2",
		PublishedAt:        &pubDate,
		SourceType:         "assemblee_law_page",
		Metadata: map[string]any{
			"law_key": lawKey1,
		},
	}
	factID2, factSourceID2, err := insertFactForTest(ctx, t, storer, input2)
	require.NoError(t, err)

	// Verify facts exist
	_ = factID1
	_ = factSourceID1
	_ = factID2
	_ = factSourceID2
	var count1 int
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_sources WHERE metadata->>'law_key' = $1", lawKey1).Scan(&count1)
	require.NoError(t, err)
	assert.Equal(t, 2, count1, "Should have 2 fact_sources for law 16-9001")

	// Case 2: Delete partial law data (law NOT in processed_laws)
	deletedCount, err := storer.DeletePartialLawData(ctx, lawKey1)
	require.NoError(t, err, "DeletePartialLawData should succeed")
	assert.Equal(t, 2, deletedCount, "Should have deleted 2 facts")

	// Verify facts are gone
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_sources WHERE metadata->>'law_key' = $1", lawKey1).Scan(&count1)
	require.NoError(t, err)
	assert.Equal(t, 0, count1, "Should have 0 fact_sources after deletion")

	var factExists bool
	err = storer.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM facts WHERE id = $1)", factID1).Scan(&factExists)
	require.NoError(t, err)
	assert.False(t, factExists, "Fact 1 should be deleted")

	err = storer.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM facts WHERE id = $1)", factID2).Scan(&factExists)
	require.NoError(t, err)
	assert.False(t, factExists, "Fact 2 should be deleted")

	// Case 3: Create facts for law 16-9002 and mark it as complete
	input3 := StoreFactInput{
		SubjectEntity:      "Test Person",
		NormalizedFactText: "Complete fact for law 16-9002",
		Embedding:          mockEmbedding(),
		OriginalQuote:      "Quote 3",
		CanonicalVerb:      "",
		SourceURL:          "https://test.com/9002",
		PublishedAt:        &pubDate,
		SourceType:         "assemblee_law_page",
		Metadata: map[string]any{
			"law_key": lawKey2,
		},
	}
	factID3, _, err := insertFactForTest(ctx, t, storer, input3)
	require.NoError(t, err)

	// Mark law as processed
	err = storer.MarkLawAsProcessed(ctx, lawKey2, 16, "Complete Law", 1)
	require.NoError(t, err)

	// Case 4: Try to delete - should NOT delete because law is marked complete
	deletedCount, err = storer.DeletePartialLawData(ctx, lawKey2)
	require.NoError(t, err, "DeletePartialLawData should succeed (but not delete)")
	assert.Equal(t, 0, deletedCount, "Should NOT delete facts for complete law")

	// Verify fact still exists
	err = storer.pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM facts WHERE id = $1)", factID3).Scan(&factExists)
	require.NoError(t, err)
	assert.True(t, factExists, "Fact 3 should still exist (complete law protection)")

	var count2 int
	err = storer.pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_sources WHERE metadata->>'law_key' = $1", lawKey2).Scan(&count2)
	require.NoError(t, err)
	assert.Equal(t, 1, count2, "Should still have 1 fact_source for complete law")
}

func TestComputeHonestyScore(t *testing.T) {
	// Each test fully bundles: bundles == total, so the extras penalty is zero and the
	// formula reduces to the legacy exp(-4*ratio - 0.02*contradictions).
	tests := []struct {
		name        string
		facts       int
		bundles     int
		total       int
		expectedMin float64
		expectedMax float64
	}{
		{"zero facts returns 1.0", 0, 0, 0, 1.0, 1.0},
		{"zero contradictions returns 1.0", 200, 0, 0, 1.0, 1.0},
		{"5/200 ~ 82%", 200, 5, 5, 0.81, 0.83},
		{"14/200 ~ 57%", 200, 14, 14, 0.56, 0.58},
		{"20/200 ~ 45%", 200, 20, 20, 0.44, 0.46},
		{"50/200 ~ 14%", 200, 50, 50, 0.13, 0.15},
		{"100/200 ~ 2%", 200, 100, 100, 0.01, 0.03},
		{"ratio > 1 still works", 10, 20, 20, 0.0, 0.001},
		{"all contradicted approaches zero", 10, 10, 10, 0.01, 0.02},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			score := ComputeHonestyScore(tt.facts, ContradictionStats{Bundles: tt.bundles, Total: tt.total})
			assert.GreaterOrEqual(t, score, tt.expectedMin, "score too low")
			assert.LessOrEqual(t, score, tt.expectedMax, "score too high")
		})
	}
}

// TestComputeHonestyScore_BundlingReducesPenalty asserts the core property of bundling:
// for any N >= 2, replacing N independent pairs with one bundle of size N strictly raises
// the score (i.e. the penalty 0.02 + 0.05*ln(N) is strictly less than the legacy 0.02*N).
func TestComputeHonestyScore_BundlingReducesPenalty(t *testing.T) {
	const facts = 200
	for _, n := range []int{2, 3, 5, 10, 20, 40} {
		flat := ComputeHonestyScore(facts, ContradictionStats{Bundles: n, Total: n})
		bundled := ComputeHonestyScore(facts, ContradictionStats{Bundles: 1, Total: n})
		assert.Greater(t, bundled, flat, "bundling N=%d pairs should improve the score", n)
	}
}

func TestComputeAggregateScore(t *testing.T) {
	tests := []struct {
		name         string
		ownScore     float64
		membersScore *float64
		expected     float64
	}{
		{"nil members returns own", 0.8, nil, 0.8},
		{"50/50 blend", 0.8, ptrFloat64(0.6), 0.7},
		{"both perfect", 1.0, ptrFloat64(1.0), 1.0},
		{"both zero", 0.0, ptrFloat64(0.0), 0.0},
		{"own high members low", 1.0, ptrFloat64(0.0), 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeAggregateScore(tt.ownScore, tt.membersScore)
			assert.InDelta(t, tt.expected, got, 0.001)
		})
	}
}

func ptrFloat64(v float64) *float64 {
	return &v
}

// TestStanceBundledContradictions exercises the bundling layer end-to-end: facts are placed
// into stances, contradictions are recorded between them, and the stats / list endpoints are
// expected to count distinct bundles (not raw pairs). Cross-stance and unstanced rows behave
// as singleton solo bundles.
func TestStanceBundledContradictions(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	// One person, six facts. We bypass FactInserter (LLM dedup) and insert directly via SQL,
	// because this test only cares about contradiction-bundle behavior, not the ingestion
	// pipeline. Direct inserts also avoid flakiness from mock-embedding similarity dedup.
	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('person bundle', 'human') RETURNING id`,
	).Scan(&personID))

	insertFact := func(text string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id`,
			text, pgvector.NewVector(mockEmbedding()),
		).Scan(&id))
		_, err := storer.pool.Exec(ctx,
			`INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type) VALUES ($1, $2, $3, 'test')`,
			id, fmt.Sprintf("http://ex/%d", id), text)
		require.NoError(t, err)
		_, err = storer.pool.Exec(ctx,
			`INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $2 FROM fact_sources WHERE fact_id = $1 ORDER BY id DESC LIMIT 1`,
			id, personID)
		require.NoError(t, err)
		return id
	}
	f1 := insertFact("F1: promised X.")
	f2 := insertFact("F2: admitted X failed.")
	f3 := insertFact("F3: said X impossible.")
	f4 := insertFact("F4: separate topic Y position.")
	f5 := insertFact("F5: separate topic Y reversal.")
	f6 := insertFact("F6: orphan claim no stance.")

	// Two stances. f1/f2/f3 -> stance A (the "promise X" cluster); f4/f5 -> stance B; f6 unlinked.
	stanceA, err := storer.InsertStanceDocument(ctx, StanceDocument{
		PersonID: personID, ClusterLabel: "Promise about X",
		ClusterEmbedding: mockEmbedding(),
		Summary:          "Person Bundle's promise on X and how it played out.",
		KeyFactIDs:       []int64{f1}, FactCount: 3,
	})
	require.NoError(t, err)
	stanceB, err := storer.InsertStanceDocument(ctx, StanceDocument{
		PersonID: personID, ClusterLabel: "Topic Y stance",
		ClusterEmbedding: mockEmbedding(),
		Summary:          "Position on Y.",
		KeyFactIDs:       []int64{f4}, FactCount: 2,
	})
	require.NoError(t, err)
	for _, fid := range []int64{f1, f2, f3} {
		require.NoError(t, storer.AddFactToStance(ctx, stanceA, fid))
	}
	for _, fid := range []int64{f4, f5} {
		require.NoError(t, storer.AddFactToStance(ctx, stanceB, fid))
	}

	// Record three same-stance pairs in stance A (should all bundle to stanceA),
	// one cross-stance pair (f1 in A vs f4 in B → solo),
	// and one half-unstanced pair (f1 in A vs f6 → solo).
	rec := func(a, b int64) (int64, bool) {
		t.Helper()
		id, needsBundler, err := storer.RecordContradictionImmediate(ctx, personID, a, b, fmt.Sprintf("clash %d/%d", a, b))
		require.NoError(t, err)
		return id, needsBundler
	}
	_, n12 := rec(f1, f2)
	_, n13 := rec(f1, f3)
	_, n23 := rec(f2, f3)
	_, n14 := rec(f1, f4)
	_, n16 := rec(f1, f6)

	assert.False(t, n12, "f1/f2 share stance A — fast path should set bundle")
	assert.False(t, n13, "f1/f3 share stance A — fast path should set bundle")
	assert.False(t, n23, "f2/f3 share stance A — fast path should set bundle")
	assert.True(t, n14, "f1 (A) vs f4 (B) cross-stance — bundler should be invoked")
	assert.True(t, n16, "f1 (A) vs f6 (no stance) — bundler should be invoked")

	tx, err := storer.pool.Begin(ctx)
	require.NoError(t, err)
	defer tx.Rollback(ctx)
	stats, err := storer.getPersonContradictionStats(ctx, tx, personID)
	require.NoError(t, err)

	assert.Equal(t, 5, stats.Total, "raw contradiction count")
	// Bundles: stanceA (the 3 stance-A pairs collapse) + 2 solo rows = 3.
	assert.Equal(t, 3, stats.Bundles, "should collapse 3 stance-A pairs into one bundle, plus 2 solo")

	// Score sanity: bundled (3 bundles, 5 total) must beat the legacy flat (5 bundles, 5 total).
	factsCount, err := storer.getPersonFactsCount(ctx, tx, personID)
	require.NoError(t, err)
	bundled := ComputeHonestyScore(factsCount, stats)
	flat := ComputeHonestyScore(factsCount, ContradictionStats{Bundles: 5, Total: 5})
	assert.Greater(t, bundled, flat, "bundling should improve the score vs. counting each pair")

	// GetPersonContradictions: bundle fields populated and bundle_size accurate.
	page, err := storer.GetPersonContradictions(ctx, personID, 50, 0)
	require.NoError(t, err)
	require.Len(t, page.Items, 5)

	stanceASize := 0
	for _, c := range page.Items {
		if c.BundleID != nil && *c.BundleID == stanceA {
			stanceASize++
			require.NotNil(t, c.BundleLabel)
			assert.Equal(t, "Promise about X", *c.BundleLabel)
			assert.Equal(t, 3, c.BundleSize, "stance-A bundle should report size=3")
		} else {
			assert.Equal(t, 1, c.BundleSize, "solo row reports size=1")
			assert.Nil(t, c.BundleID, "non-stance-A rows have no bundle_id")
		}
	}
	assert.Equal(t, 3, stanceASize, "exactly three rows in stance-A bundle")

	// GetStanceContradictionCounts: only stance A should appear (3 same-stance pairs).
	// The cross-stance and unstanced solos have NULL bundle_stance_id and are excluded.
	counts, err := storer.GetStanceContradictionCounts(ctx, personID)
	require.NoError(t, err)
	assert.Equal(t, 3, counts[stanceA], "stance A has 3 bundled contradictions")
	_, hasB := counts[stanceB]
	assert.False(t, hasB, "stance B has no bundled contradictions (the only B-side pair is cross-stance solo)")

	// GetStanceContradictions: stance A returns its 3 rows, stance B returns none.
	stanceAItems, err := storer.GetStanceContradictions(ctx, stanceA)
	require.NoError(t, err)
	assert.Len(t, stanceAItems, 3, "stance A has 3 contradictions")
	for _, c := range stanceAItems {
		require.NotNil(t, c.BundleID)
		assert.Equal(t, stanceA, *c.BundleID)
		require.NotNil(t, c.BundleLabel)
		assert.Equal(t, "Promise about X", *c.BundleLabel)
		assert.Equal(t, 3, c.BundleSize)
	}
	stanceBItems, err := storer.GetStanceContradictions(ctx, stanceB)
	require.NoError(t, err)
	assert.Empty(t, stanceBItems, "stance B has no contradictions")
}
