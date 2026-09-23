package storage

import (
	"context"
	"digital-mirror/backend/core"
	"strings"
	"testing"
	"time"

	pgvector "github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// truncateFanoutTables clears the fixture tables, retrying on deadlock: the
// background stance-invariant collector polls these tables, and a multi-table
// TRUNCATE racing its SELECT can hit 40P01 under `go test -p 1` suite runs.
func truncateFanoutTables(t *testing.T, storer *PostgresStorer) {
	t.Helper()
	ctx := context.Background()
	const stmt = "TRUNCATE TABLE stance_document_facts, stance_documents, fact_mentions, fact_sources, fact_relations, facts RESTART IDENTITY CASCADE"
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if _, err = storer.pool.Exec(ctx, stmt); err == nil {
			return
		}
		if !strings.Contains(err.Error(), "40P01") && !strings.Contains(err.Error(), "deadlock") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	require.NoError(t, err)
}

// TestStanceFanoutPendingSet covers the person-scoped orphan derivation that
// drives the stance fan-out (mirror-py3): a shared vote fact mentioned by
// three persons but stance-routed only for its subject must surface as an
// orphan for BOTH co-actors (the old unscoped queries considered it globally
// stanced), duplicates and embedding-less facts must be excluded, and routing
// a pair must remove it (idempotent, self-terminating). Also pins
// CountPendingStanceRoutings to the same definition.
func TestStanceFanoutPendingSet(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	truncateFanoutTables(t, storer)

	subjectID, _, err := storer.AddPerson(ctx, "Fanout Subject")
	require.NoError(t, err)
	coActorA, _, err := storer.AddPerson(ctx, "Fanout CoActor A")
	require.NoError(t, err)
	coActorB, _, err := storer.AddPerson(ctx, "Fanout CoActor B")
	require.NoError(t, err)

	emb := pgvector.NewVector(makeUnitEmbedding(0))
	insertFact := func(text string, withEmbedding bool) int64 {
		var id int64
		if withEmbedding {
			require.NoError(t, storer.pool.QueryRow(ctx,
				"INSERT INTO facts (fact_text, canonical_verb, embedding) VALUES ($1, 'voted', $2) RETURNING id",
				text, emb).Scan(&id))
		} else {
			require.NoError(t, storer.pool.QueryRow(ctx,
				"INSERT INTO facts (fact_text) VALUES ($1) RETURNING id", text).Scan(&id))
		}
		return id
	}
	mention := func(factID int64, personIDs ...int64) {
		var sourceID int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO fact_sources (fact_id, source_url, source_type) VALUES ($1, 'https://example.test', 'assemblee_law_page') RETURNING id",
			factID).Scan(&sourceID))
		for _, pid := range personIDs {
			_, err := storer.pool.Exec(ctx,
				"INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sourceID, pid)
			require.NoError(t, err)
		}
	}
	addStanceWithFact := func(personID, factID int64, label string) int64 {
		stanceID, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
			PersonID:         personID,
			ClusterLabel:     label,
			ClusterEmbedding: makeUnitEmbedding(0),
			Summary:          "s",
			KeyFactIDs:       []int64{factID},
			FactCount:        1,
		})
		require.NoError(t, err)
		require.NoError(t, storer.AddFactToStance(ctx, stanceID, factID))
		return stanceID
	}

	// Shared vote fact: three mentions, stance membership for the subject only.
	voteFact := insertFact("a voté POUR le texte de loi : « Loi A »", true)
	mention(voteFact, subjectID, coActorA, coActorB)
	addStanceWithFact(subjectID, voteFact, "loi A")

	// Duplicate-marked fact: mentioned by a co-actor but must never be pending.
	dupFact := insertFact("duplicate member", true)
	mention(dupFact, coActorA)
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')",
		voteFact, dupFact)
	require.NoError(t, err)

	// Embedding-less fact: cannot be routed, must not surface.
	noEmbFact := insertFact("no embedding yet", false)
	mention(noEmbFact, coActorB)

	// Both co-actors are orphan-carriers; the subject is fully routed.
	persons, err := storer.ListPersonsWithOrphanFacts(ctx)
	require.NoError(t, err)
	personIDs := make(map[int64]bool)
	for _, p := range persons {
		personIDs[p.ID] = true
	}
	require.True(t, personIDs[coActorA], "co-actor A must be listed (shared fact not stanced for them)")
	require.True(t, personIDs[coActorB], "co-actor B must be listed")
	require.False(t, personIDs[subjectID], "subject already routed, not listed")

	// The shared fact is an orphan FOR EACH co-actor despite the subject's stance.
	orphansA, err := storer.ListOrphanFactsForPerson(ctx, coActorA)
	require.NoError(t, err)
	require.Len(t, orphansA, 1)
	require.Equal(t, voteFact, orphansA[0].ID)
	require.NotEmpty(t, orphansA[0].FactText)
	require.NotEmpty(t, orphansA[0].Embedding)

	orphansB, err := storer.ListOrphanFactsForPerson(ctx, coActorB)
	require.NoError(t, err)
	require.Len(t, orphansB, 1)
	require.Equal(t, voteFact, orphansB[0].ID)

	count, err := storer.CountPendingStanceRoutings(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, count, "one pending routing per unrouted co-actor")

	// Routing the fact for co-actor B removes exactly B's pending pair.
	addStanceWithFact(coActorB, voteFact, "loi A")
	orphansB, err = storer.ListOrphanFactsForPerson(ctx, coActorB)
	require.NoError(t, err)
	require.Empty(t, orphansB, "routed pair leaves the pending set")
	count, err = storer.CountPendingStanceRoutings(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, count)

	persons, err = storer.ListPersonsWithOrphanFacts(ctx)
	require.NoError(t, err)
	personIDs = map[int64]bool{}
	for _, p := range persons {
		personIDs[p.ID] = true
	}
	require.True(t, personIDs[coActorA])
	require.False(t, personIDs[coActorB])
}

// TestGetFactStanceMapPersonScoped pins the person-scoped fact→stance lookup:
// with a shared fact in two persons' stances, each person resolves to their OWN
// stance, never an arbitrary one.
func TestGetFactStanceMapPersonScoped(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	truncateFanoutTables(t, storer)

	personP, _, err := storer.AddPerson(ctx, "Scoped P")
	require.NoError(t, err)
	personQ, _, err := storer.AddPerson(ctx, "Scoped Q")
	require.NoError(t, err)

	var factID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		"INSERT INTO facts (fact_text) VALUES ('shared vote') RETURNING id").Scan(&factID))

	mkStance := func(personID int64, label string) int64 {
		id, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
			PersonID: personID, ClusterLabel: label,
			ClusterEmbedding: makeUnitEmbedding(0), Summary: "s",
			KeyFactIDs: []int64{factID}, FactCount: 1,
		})
		require.NoError(t, err)
		require.NoError(t, storer.AddFactToStance(ctx, id, factID))
		return id
	}
	stanceP := mkStance(personP, "p-topic")
	stanceQ := mkStance(personQ, "q-topic")

	// A third person carrying no membership resolves to none.
	personR, _, err := storer.AddPerson(ctx, "Scoped R")
	require.NoError(t, err)

	m, err := storer.GetFactStanceMap(ctx, personP, []int64{factID})
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{factID: stanceP}, m)
	m, err = storer.GetFactStanceMap(ctx, personQ, []int64{factID})
	require.NoError(t, err)
	require.Equal(t, map[int64]int64{factID: stanceQ}, m)
	m, err = storer.GetFactStanceMap(ctx, personR, []int64{factID})
	require.NoError(t, err)
	require.Empty(t, m)
}
