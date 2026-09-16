package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestDebateDocumentsPendingSet pins the débats idempotence primitive: a
// document is pending until recorded (even with zero facts), identity is
// (cr_uid, actor_id) — the source URL may change without re-extraction —
// recording is an upsert, and the assemblee_id → person map only covers
// persons carrying the attribute and picks one row per id deterministically.
func TestDebateDocumentsPendingSet(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE debate_documents")
	require.NoError(t, err)
	storer.SetEntityValidator(nil)

	personID, _, err := storer.AddPerson(ctx, "Debate Speaker")
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx,
		`UPDATE persons SET attributes = COALESCE(attributes, '{}'::jsonb) || '{"assemblee_id":"PA999001"}'::jsonb WHERE id = $1`, personID)
	require.NoError(t, err)
	otherID, _, err := storer.AddPerson(ctx, "Debate Nobody")
	require.NoError(t, err)
	// A duplicate row carrying the same actor id but fewer facts must lose.
	dupID, _, err := storer.AddPerson(ctx, "Debate Speaker Dup")
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx,
		`UPDATE persons SET attributes = '{"assemblee_id":"PA999001"}'::jsonb, fact_count = 0 WHERE id = $1`, dupID)
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx, `UPDATE persons SET fact_count = 5 WHERE id = $1`, personID)
	require.NoError(t, err)

	byAID, err := storer.GetPersonsByAssembleeID(ctx)
	require.NoError(t, err)
	require.Equal(t, personID, byAID["PA999001"].ID, "the row with more facts wins the id")
	require.Equal(t, "debate speaker", byAID["PA999001"].Name)
	for _, p := range byAID {
		require.NotEqual(t, otherID, p.ID, "persons without assemblee_id are not in the map")
	}

	keys := []DebateDocumentKey{
		{CRUID: "CRSANR5L17S2026O1N001", ActorID: "PA999001"},
		{CRUID: "CRSANR5L17S2026O1N001", ActorID: "PA999002"},
		{CRUID: "CRSANR5L17S2026O1N002", ActorID: "PA999001"},
	}
	pending, err := storer.FilterUnprocessedDebateDocuments(ctx, keys)
	require.NoError(t, err)
	require.Equal(t, keys, pending, "nothing recorded yet: everything pending, input order kept")

	// A zero-fact document is still recorded and leaves the pending set.
	day := time.Date(2026, 1, 26, 0, 0, 0, 0, time.UTC)
	require.NoError(t, storer.RecordDebateDocument(ctx, DebateDocumentRecord{
		DebateDocumentKey: keys[1], SourceURL: "https://an.test/seance-1#200", PersonID: personID,
		SeanceDate: day, Paragraphs: 2, Chars: 900, FactsCount: 0,
	}))
	pending, err = storer.FilterUnprocessedDebateDocuments(ctx, keys)
	require.NoError(t, err)
	require.Equal(t, []DebateDocumentKey{keys[0], keys[2]}, pending)

	// Upsert: recording again with a moved anchor URL updates the row.
	require.NoError(t, storer.RecordDebateDocument(ctx, DebateDocumentRecord{
		DebateDocumentKey: keys[1], SourceURL: "https://an.test/seance-1#250", PersonID: personID,
		SeanceDate: day, Paragraphs: 3, Chars: 1200, FactsCount: 3, FactsDropped: 1,
	}))
	var n, dropped int
	var u string
	require.NoError(t, storer.pool.QueryRow(ctx,
		"SELECT facts_count, facts_dropped, source_url FROM debate_documents WHERE cr_uid = $1 AND actor_id = $2", keys[1].CRUID, keys[1].ActorID).Scan(&n, &dropped, &u))
	require.Equal(t, 3, n)
	require.Equal(t, 1, dropped)
	require.Equal(t, "https://an.test/seance-1#250", u)
	pending, err = storer.FilterUnprocessedDebateDocuments(ctx, keys)
	require.NoError(t, err)
	require.Len(t, pending, 2, "same identity: still processed after the URL moved")

	empty, err := storer.FilterUnprocessedDebateDocuments(ctx, nil)
	require.NoError(t, err)
	require.Nil(t, empty)
}

// GetPersonPressFactCounts counts active news/twitter facts per person and
// ignores vote/transcript sources and duplicate-marked facts.
func TestGetPersonPressFactCounts(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	truncateFanoutTables(t, storer)
	storer.SetEntityValidator(nil)

	pressy, _, err := storer.AddPerson(ctx, "Press Heavy")
	require.NoError(t, err)
	quiet, _, err := storer.AddPerson(ctx, "Press Quiet")
	require.NoError(t, err)
	newFact := func() int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx, "INSERT INTO facts (fact_text) VALUES ('f') RETURNING id").Scan(&id))
		return id
	}
	n := 0
	src := func(factID int64, sourceType string, person int64) {
		n++
		var sid int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO fact_sources (fact_id, source_url, source_type) VALUES ($1, $2, $3) RETURNING id",
			factID, "https://s.test/"+string(rune('a'+n)), sourceType).Scan(&sid))
		_, err := storer.pool.Exec(ctx, "INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sid, person)
		require.NoError(t, err)
	}
	f1, f2, f3, f4, f5 := newFact(), newFact(), newFact(), newFact(), newFact()
	src(f1, "news", pressy)
	src(f2, "twitter", pressy)
	src(f3, "assemblee_law_page", pressy) // votes do not count
	src(f4, "assemblee_debat", pressy)    // transcripts do not count
	src(f5, "news", pressy)               // duplicate-marked: does not count
	_, err = storer.pool.Exec(ctx, "INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')", f1, f5)
	require.NoError(t, err)
	src(newFact(), "assemblee_law_page", quiet)

	counts, err := storer.GetPersonPressFactCounts(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, counts[pressy])
	_, has := counts[quiet]
	require.False(t, has, "no press facts → absent (priority 0)")
}
