package frpolitics

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"digital-mirror/backend/storage"

	"github.com/jackc/pgx/v5"
	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupStoreTest connects to the test database (skipped without
// DATABASE_URL), opens it without a person gate (fixture names are
// synthetic and the real validator would reject them) and clears the tables the
// plugin's DB tests touch. Like the storage test harness, the repo-root .env
// wins over the shell so `supabase status` output pasted with its quotes
// still connects.
func setupStoreTest(t *testing.T) (*storage.PostgresStorer, *Store) {
	t.Helper()
	if err := godotenv.Overload("../../../.env"); err != nil {
		t.Logf("no .env loaded (%v); relying on the environment", err)
	}
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set; start Supabase and set env to run this test")
	}
	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err, "connecting to the test database")
	t.Cleanup(storer.Close)
	_, err = storer.Pool().Exec(ctx, "TRUNCATE TABLE source_documents, processed_laws, fact_relations, fact_mentions, fact_sources, facts, contradictions, person_links, persons RESTART IDENTITY CASCADE")
	require.NoError(t, err, "truncating tables")
	return storer, NewStore(storer)
}

// The assemblee_id → person map only covers persons carrying the attribute
// and picks one row per id deterministically.
func TestGetPersonsByAssembleeID(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()
	pool := storer.Pool()

	personID, _, err := storer.AddPerson(ctx, "Debate Speaker")
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`UPDATE persons SET attributes = COALESCE(attributes, '{}'::jsonb) || '{"assemblee_id":"PA999001"}'::jsonb WHERE id = $1`, personID)
	require.NoError(t, err)
	otherID, _, err := storer.AddPerson(ctx, "Debate Nobody")
	require.NoError(t, err)
	// A duplicate row carrying the same actor id but fewer facts must lose.
	dupID, _, err := storer.AddPerson(ctx, "Debate Speaker Dup")
	require.NoError(t, err)
	_, err = pool.Exec(ctx,
		`UPDATE persons SET attributes = '{"assemblee_id":"PA999001"}'::jsonb, fact_count = 0 WHERE id = $1`, dupID)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE persons SET fact_count = 5 WHERE id = $1`, personID)
	require.NoError(t, err)

	byAID, err := store.GetPersonsByAssembleeID(ctx)
	require.NoError(t, err)
	require.Equal(t, personID, byAID["PA999001"].ID, "the row with more facts wins the id")
	require.Equal(t, "debate speaker", byAID["PA999001"].Name)
	for _, p := range byAID {
		require.NotEqual(t, otherID, p.ID, "persons without assemblee_id are not in the map")
	}
}

// GetPersonFactCountsBySourceTypes counts active facts of the given source
// types per person and ignores other sources and duplicate-marked facts.
func TestGetPersonFactCountsBySourceTypes(t *testing.T) {
	storer, _ := setupStoreTest(t)
	ctx := context.Background()
	pool := storer.Pool()

	pressy, _, err := storer.AddPerson(ctx, "Press Heavy")
	require.NoError(t, err)
	quiet, _, err := storer.AddPerson(ctx, "Press Quiet")
	require.NoError(t, err)
	newFact := func() int64 {
		var id int64
		require.NoError(t, pool.QueryRow(ctx, "INSERT INTO facts (fact_text) VALUES ('f') RETURNING id").Scan(&id))
		return id
	}
	n := 0
	src := func(factID int64, sourceType string, person int64) {
		n++
		var sid int64
		require.NoError(t, pool.QueryRow(ctx,
			"INSERT INTO fact_sources (fact_id, source_url, source_type) VALUES ($1, $2, $3) RETURNING id",
			factID, "https://s.test/"+string(rune('a'+n)), sourceType).Scan(&sid))
		_, err := pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sid, person)
		require.NoError(t, err)
	}
	f1, f2, f3, f4, f5 := newFact(), newFact(), newFact(), newFact(), newFact()
	src(f1, "news", pressy)
	src(f2, "twitter", pressy)
	src(f3, "assemblee_law_page", pressy) // votes do not count
	src(f4, "assemblee_debat", pressy)    // transcripts do not count
	src(f5, "news", pressy)               // duplicate-marked: does not count
	_, err = pool.Exec(ctx, "INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')", f1, f5)
	require.NoError(t, err)
	src(newFact(), "assemblee_law_page", quiet)

	counts, err := storer.GetPersonFactCountsBySourceTypes(ctx, pressSourceTypes)
	require.NoError(t, err)
	require.Equal(t, 2, counts[pressy])
	_, has := counts[quiet]
	require.False(t, has, "no press facts → absent (priority 0)")
}

// TestStoreProcessedLawsTracking covers the processed_laws completion marker:
// absent until marked, upsert on re-marking.
func TestStoreProcessedLawsTracking(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()
	pool := storer.Pool()

	lawKey1 := "16-1234"
	lawKey2 := "16-5678"

	// processedLaw reads the marker row; found=false when there is none.
	type processedLaw struct {
		legislature int
		title       string
		factsCount  int
		processedAt time.Time
	}
	processedLawFor := func(lawKey string) (processedLaw, bool) {
		var info processedLaw
		err := pool.QueryRow(ctx, "SELECT legislature, title, facts_count, processed_at FROM processed_laws WHERE law_key = $1", lawKey).
			Scan(&info.legislature, &info.title, &info.factsCount, &info.processedAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return info, false
		}
		require.NoError(t, err)
		return info, true
	}

	// Case 1: Initially not processed
	processed, err := store.IsLawFullyProcessed(ctx, lawKey1)
	require.NoError(t, err, "IsLawFullyProcessed should not error for non-existent law")
	assert.False(t, processed, "Law should not be processed initially")

	// Case 2: Mark law as processed
	err = store.MarkLawAsProcessed(ctx, lawKey1, 16, "Test Law 1234", 42)
	require.NoError(t, err, "MarkLawAsProcessed should succeed")

	// Case 3: Now should be processed
	processed, err = store.IsLawFullyProcessed(ctx, lawKey1)
	require.NoError(t, err, "IsLawFullyProcessed should not error after marking")
	assert.True(t, processed, "Law should be marked as processed")

	// Case 4: The marker row carries the law metadata
	info, found := processedLawFor(lawKey1)
	require.True(t, found, "processed_laws row should exist")
	assert.Equal(t, 16, info.legislature, "Legislature should match")
	assert.Equal(t, "Test Law 1234", info.title, "Title should match")
	assert.Equal(t, 42, info.factsCount, "FactsCount should match")
	assert.False(t, info.processedAt.IsZero(), "ProcessedAt should be set")

	// Case 5: Different law key should not be processed
	processed, err = store.IsLawFullyProcessed(ctx, lawKey2)
	require.NoError(t, err, "IsLawFullyProcessed should not error for different law")
	assert.False(t, processed, "Different law should not be processed")

	// Case 6: No marker row for a law never marked
	_, found = processedLawFor(lawKey2)
	assert.False(t, found, "no processed_laws row for a non-existent law")

	// Case 7: Mark second law as processed
	err = store.MarkLawAsProcessed(ctx, lawKey2, 16, "Test Law 5678", 30)
	require.NoError(t, err, "MarkLawAsProcessed should succeed for second law")

	// Both should now be processed
	processed, err = store.IsLawFullyProcessed(ctx, lawKey1)
	require.NoError(t, err)
	assert.True(t, processed, "First law should still be processed")

	processed, err = store.IsLawFullyProcessed(ctx, lawKey2)
	require.NoError(t, err)
	assert.True(t, processed, "Second law should be processed")

	// Case 8: Test upsert behavior (marking same law again updates timestamp)
	time.Sleep(10 * time.Millisecond)                                     // Small delay to ensure timestamp changes
	err = store.MarkLawAsProcessed(ctx, lawKey1, 16, "Test Law 1234", 43) // Different facts_count
	require.NoError(t, err, "MarkLawAsProcessed should succeed on upsert")

	info, found = processedLawFor(lawKey1)
	require.True(t, found)
	assert.Equal(t, 43, info.factsCount, "FactsCount should be updated to new value")
}

// TestStoreDeletePartialLawData covers the cleanup of a law whose store
// failed mid-way: its facts, sources and mentions go, unless the law carries
// the processed_laws completion marker.
func TestStoreDeletePartialLawData(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()
	pool := storer.Pool()

	lawKey1 := "16-9001"
	lawKey2 := "16-9002"

	personID, _, err := storer.AddPerson(ctx, "Test Person")
	require.NoError(t, err)
	// lawFact inserts one vote fact carrying law_key in its source metadata,
	// mentioned by the test person.
	lawFact := func(text, url, lawKey string) int64 {
		var id, sid int64
		require.NoError(t, pool.QueryRow(ctx, "INSERT INTO facts (fact_text) VALUES ($1) RETURNING id", text).Scan(&id))
		require.NoError(t, pool.QueryRow(ctx,
			"INSERT INTO fact_sources (fact_id, source_url, source_type, metadata) VALUES ($1, $2, 'assemblee_law_page', jsonb_build_object('law_key', $3::text)) RETURNING id",
			id, url, lawKey).Scan(&sid))
		_, err := pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sid, personID)
		require.NoError(t, err)
		return id
	}
	countSources := func(lawKey string) int {
		var n int
		require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_sources WHERE metadata->>'law_key' = $1", lawKey).Scan(&n))
		return n
	}
	factExists := func(id int64) bool {
		var exists bool
		require.NoError(t, pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM facts WHERE id = $1)", id).Scan(&exists))
		return exists
	}

	// Case 1: Create partial facts for law 16-9001 (NOT in processed_laws)
	factID1 := lawFact("Partial fact 1 for law 16-9001", "https://test.com/9001-1", lawKey1)
	factID2 := lawFact("Partial fact 2 for law 16-9001", "https://test.com/9001-2", lawKey1)
	assert.Equal(t, 2, countSources(lawKey1), "Should have 2 fact_sources for law 16-9001")

	// Case 2: Delete partial law data (law NOT in processed_laws)
	deletedCount, err := store.DeletePartialLawData(ctx, lawKey1)
	require.NoError(t, err, "DeletePartialLawData should succeed")
	assert.Equal(t, 2, deletedCount, "Should have deleted 2 facts")

	// Verify facts are gone
	assert.Equal(t, 0, countSources(lawKey1), "Should have 0 fact_sources after deletion")
	assert.False(t, factExists(factID1), "Fact 1 should be deleted")
	assert.False(t, factExists(factID2), "Fact 2 should be deleted")
	var mentions int
	require.NoError(t, pool.QueryRow(ctx, "SELECT COUNT(*) FROM fact_mentions WHERE person_id = $1", personID).Scan(&mentions))
	assert.Equal(t, 0, mentions, "mentions of the deleted sources should be gone")

	// Case 3: Create a fact for law 16-9002 and mark it as complete
	factID3 := lawFact("Complete fact for law 16-9002", "https://test.com/9002", lawKey2)
	err = store.MarkLawAsProcessed(ctx, lawKey2, 16, "Complete Law", 1)
	require.NoError(t, err)

	// Case 4: Try to delete - should NOT delete because law is marked complete
	deletedCount, err = store.DeletePartialLawData(ctx, lawKey2)
	require.NoError(t, err, "DeletePartialLawData should succeed (but not delete)")
	assert.Equal(t, 0, deletedCount, "Should NOT delete facts for complete law")

	// Verify fact still exists
	assert.True(t, factExists(factID3), "Fact 3 should still exist (complete law protection)")
	assert.Equal(t, 1, countSources(lawKey2), "Should still have 1 fact_source for complete law")
}
