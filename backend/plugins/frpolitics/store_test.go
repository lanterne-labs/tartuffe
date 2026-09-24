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
	_, err = storer.Pool().Exec(ctx, "TRUNCATE TABLE source_documents, processed_laws, skipped_laws, fact_relations, fact_mentions, fact_sources, facts, contradictions, person_links, persons RESTART IDENTITY CASCADE")
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
// absent until marked, upsert on re-marking. A marked law is settled, and so
// is a law skipped for a verdict (skipped_laws).
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
	settled, err := store.SettledLawKeys(ctx, []string{lawKey1}, nil)
	processed := settled[lawKey1]
	require.NoError(t, err, "SettledLawKeys should not error for non-existent law")
	assert.False(t, processed, "Law should not be processed initially")

	// Case 2: Mark law as processed
	err = store.MarkLawAsProcessed(ctx, lawKey1, 16, "Test Law 1234", 42)
	require.NoError(t, err, "MarkLawAsProcessed should succeed")

	// Case 3: Now should be processed
	settled, err = store.SettledLawKeys(ctx, []string{lawKey1}, nil)
	processed = settled[lawKey1]
	require.NoError(t, err, "SettledLawKeys should not error after marking")
	assert.True(t, processed, "Law should be marked as processed")

	// Case 4: The marker row carries the law metadata
	info, found := processedLawFor(lawKey1)
	require.True(t, found, "processed_laws row should exist")
	assert.Equal(t, 16, info.legislature, "Legislature should match")
	assert.Equal(t, "Test Law 1234", info.title, "Title should match")
	assert.Equal(t, 42, info.factsCount, "FactsCount should match")
	assert.False(t, info.processedAt.IsZero(), "ProcessedAt should be set")

	// Case 5: Different law key should not be processed
	settled, err = store.SettledLawKeys(ctx, []string{lawKey2}, nil)
	processed = settled[lawKey2]
	require.NoError(t, err, "SettledLawKeys should not error for different law")
	assert.False(t, processed, "Different law should not be processed")

	// Case 6: No marker row for a law never marked
	_, found = processedLawFor(lawKey2)
	assert.False(t, found, "no processed_laws row for a non-existent law")

	// Case 7: Mark second law as processed
	err = store.MarkLawAsProcessed(ctx, lawKey2, 16, "Test Law 5678", 30)
	require.NoError(t, err, "MarkLawAsProcessed should succeed for second law")

	// Both should now be processed
	settled, err = store.SettledLawKeys(ctx, []string{lawKey1}, nil)
	processed = settled[lawKey1]
	require.NoError(t, err)
	assert.True(t, processed, "First law should still be processed")

	settled, err = store.SettledLawKeys(ctx, []string{lawKey2}, nil)
	processed = settled[lawKey2]
	require.NoError(t, err)
	assert.True(t, processed, "Second law should be processed")

	// Case 8: Test upsert behavior (marking same law again updates timestamp)
	time.Sleep(10 * time.Millisecond)                                     // Small delay to ensure timestamp changes
	err = store.MarkLawAsProcessed(ctx, lawKey1, 16, "Test Law 1234", 43) // Different facts_count
	require.NoError(t, err, "MarkLawAsProcessed should succeed on upsert")

	info, found = processedLawFor(lawKey1)
	require.True(t, found)
	assert.Equal(t, 43, info.factsCount, "FactsCount should be updated to new value")

	// Case 9: A law skipped by a filter the run applies is settled too; an
	// unknown one is not
	lowConfidence := 0.2
	require.NoError(t, store.StoreSkippedLaw(ctx, "16-9999", "Loi de finances", 16, nil, "keyword_match", "projet de loi de finances", nil))
	require.NoError(t, store.StoreSkippedLaw(ctx, "16-9998", "Loi obscure", 16, nil, "low_ai_confidence", "", &lowConfidence))
	keys := []string{lawKey1, "16-9999", "16-9998", "16-0000"}
	settled, err = store.SettledLawKeys(ctx, keys, []string{"keyword_match", "low_ai_confidence"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{lawKey1: true, "16-9999": true, "16-9998": true}, settled)

	// Case 10: A run with a filter disabled re-considers that filter's skips
	settled, err = store.SettledLawKeys(ctx, keys, []string{"low_ai_confidence"})
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{lawKey1: true, "16-9998": true}, settled, "keyword filter disabled")
	settled, err = store.SettledLawKeys(ctx, keys, nil)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{lawKey1: true}, settled, "both filters disabled")
}

// TestStoreStoredLawSubjects: a law whose store failed part-way resumes
// after the voters whose ballot is stored for it, and only for that law. The
// voter is matched like the inserter resolves it (normalized, accent- and
// case-folded), whatever the fact text says.
func TestStoreStoredLawSubjects(t *testing.T) {
	storer, store := setupStoreTest(t)
	ctx := context.Background()
	pool := storer.Pool()

	ballot := func(voter, text, sourceType, lawKey string) {
		personID, _, err := storer.AddPerson(ctx, voter)
		require.NoError(t, err)
		var factID, sourceID int64
		require.NoError(t, pool.QueryRow(ctx, "INSERT INTO facts (fact_text) VALUES ($1) RETURNING id", text).Scan(&factID))
		require.NoError(t, pool.QueryRow(ctx,
			"INSERT INTO fact_sources (fact_id, source_url, source_type, metadata) VALUES ($1, $2, $3, jsonb_build_object('law_key', $4::text)) RETURNING id",
			factID, "https://test.com/"+lawKey, sourceType, lawKey).Scan(&sourceID))
		_, err = pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sourceID, personID)
		require.NoError(t, err)
	}
	ballot("Jean Dupont", "Jean Dupont a voté POUR le texte de loi : « Renforce X »", "assemblee_law_page", "16-9001")
	ballot("Rassemblement National", "Rassemblement National a collectivement voté CONTRE le texte", "assemblee_law_page", "16-9001")
	ballot("Marie Martin", "Marie Martin a voté POUR le texte 9002", "assemblee_law_page", "16-9002")
	ballot("Paul Durand", "Paul Durand a parlé du texte 9001", "assemblee_debat", "16-9001")

	subjects := []string{"Jean DUPONT", "Rassemblement National", "Marie Martin", "Paul Durand", "Élise Neuve"}
	stored, err := store.StoredLawSubjects(ctx, "16-9001", subjects)
	require.NoError(t, err)
	assert.Equal(t, map[string]bool{"Jean DUPONT": true, "Rassemblement National": true}, stored)

	none, err := store.StoredLawSubjects(ctx, "16-9999", subjects)
	require.NoError(t, err)
	assert.Empty(t, none)
}
