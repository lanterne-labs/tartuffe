package storage

import (
	"context"
	"digital-mirror/backend/core"
	"testing"
	"time"

	pgvector "github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetPersonRecordByName pins the indexed exact lookup ResolveTargetName
// tries first: accent- and case-insensitive on the canonical person form,
// entity_type defaulted like GetAllPersonRecords, found=false (no error) on a
// miss and on an article-only variant (that one is the full-scan's job).
func TestGetPersonRecordByName(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	id, _, err := storer.AddPersonWithType(ctx, "Jean-Luc Mélenchon", "human")
	require.NoError(t, err)
	var untypedID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('untyped person', NULL) RETURNING id`).Scan(&untypedID))

	rec, found, err := storer.GetPersonRecordByName(ctx, "JEAN-LUC MELENCHON")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, core.PersonRecord{ID: id, Name: "jean-luc melenchon", EntityType: "human"}, rec)

	rec, found, err = storer.GetPersonRecordByName(ctx, "untyped person")
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, untypedID, rec.ID)
	assert.Equal(t, "person", rec.EntityType, "NULL entity_type defaults like GetAllPersonRecords")

	_, found, err = storer.GetPersonRecordByName(ctx, "Jean-Luc Mélenchon le")
	require.NoError(t, err)
	assert.False(t, found, "article variants are not an indexed hit")

	_, found, err = storer.GetPersonRecordByName(ctx, "nobody here")
	require.NoError(t, err)
	assert.False(t, found)
}

// TestGetPersonIDsByAttribute covers the batch attribute lookup: one query
// resolves every value, unknown values are absent, and the lowest id wins
// when two persons carry the same value.
func TestGetPersonIDsByAttribute(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	insert := func(name, assembleeID string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO persons (name, entity_type, attributes)
			 VALUES ($1, 'human', jsonb_build_object('assemblee_id', $2::text)) RETURNING id`,
			name, assembleeID).Scan(&id))
		return id
	}
	a := insert("deputy attr a", "PA1")
	b := insert("deputy attr b", "PA2")
	insert("deputy attr b bis", "PA2")

	got, err := storer.GetPersonIDsByAttribute(ctx, "assemblee_id", []string{"PA1", "PA2", "PA404"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"PA1": a, "PA2": b}, got)

	// Agrees with the single lookup.
	single, err := storer.GetPersonIDByAttribute(ctx, "assemblee_id", "PA1")
	require.NoError(t, err)
	assert.Equal(t, single, got["PA1"])

	empty, err := storer.GetPersonIDsByAttribute(ctx, "assemblee_id", nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestGetPersonIDsByNames covers the batch name lookup: keyed by the names as
// given, matched through the canonical person form like GetPersonIDByName,
// unknown names absent.
func TestGetPersonIDsByNames(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	a, _, err := storer.AddPersonWithType(ctx, "éric ciotti", "human")
	require.NoError(t, err)
	b, _, err := storer.AddPersonWithType(ctx, "marine le pen", "human")
	require.NoError(t, err)

	got, err := storer.GetPersonIDsByNames(ctx, []string{"Éric Ciotti", "marine le pen", "nobody at all"})
	require.NoError(t, err)
	assert.Equal(t, map[string]int64{"Éric Ciotti": a, "marine le pen": b}, got)

	single, err := storer.GetPersonIDByName(ctx, "Éric Ciotti")
	require.NoError(t, err)
	assert.Equal(t, single, got["Éric Ciotti"])

	empty, err := storer.GetPersonIDsByNames(ctx, nil)
	require.NoError(t, err)
	assert.Empty(t, empty)
}

// TestFindSimilarActiveFactsCarriesVerbAndMentions pins the two per-candidate
// lookups the inserter used to make: each hit carries its canonical verb and
// the number of distinct persons mentioning it.
func TestFindSimilarActiveFactsCarriesVerbAndMentions(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, _, err := storer.AddPersonWithType(ctx, "similar subject", "human")
	require.NoError(t, err)
	coActor, _, err := storer.AddPersonWithType(ctx, "similar co actor", "human")
	require.NoError(t, err)

	emb := makeUnitEmbedding(0)
	factID, _, err := insertFactForTest(ctx, t, storer, core.StoreFactInput{
		SubjectEntity: "similar subject", SubjectEntityType: "human", Lang: "fr",
		NormalizedFactText: "a voté pour le texte", EmbeddingText: "vote texte",
		Embedding: emb, CanonicalVerb: "VOTED_FOR", CoActorIDs: []int64{coActor},
		SourceURL: "http://example.com/vote", SourceType: "assemblee_law_page",
	})
	require.NoError(t, err)

	similar, err := storer.FindSimilarActiveFacts(ctx, []int64{pid}, emb, 0.2, "", 0, 5)
	require.NoError(t, err)
	require.Len(t, similar, 1)
	assert.Equal(t, factID, similar[0].ID)
	assert.Equal(t, "VOTED_FOR", similar[0].CanonicalVerb)
	assert.Equal(t, 2, similar[0].MentionPersons, "subject + co-actor")
}

// TestGetNeighborPairsForPerson pins the one-statement candidate walk against
// the shape the per-fact walk handled: KNN neighbors within the L2 threshold,
// capped to the 30 nearest before the id filter, the duplicate-marked fact
// excluded, the partner replaced by its most general implication ancestor
// (text and earliest source date included), fact-id then distance order; and
// check-all mode pairing every active fact with every other, distance 0.
func TestGetNeighborPairsForPerson(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	truncateFanoutTables(t, storer)

	pid, _, err := storer.AddPerson(ctx, "Neighbor Subject")
	require.NoError(t, err)

	// Embeddings: a base unit vector shifted along another axis by a known
	// amount, so L2 distances are exact and the far fact sits past the
	// threshold (0.8).
	shifted := func(axis int, by float32) []float32 {
		emb := makeUnitEmbedding(0)
		emb[axis] = by
		return emb
	}
	date := func(day int) time.Time {
		return time.Date(2026, 1, day, 0, 0, 0, 0, time.UTC)
	}
	insert := func(text, verb string, emb []float32, published *time.Time, mention bool) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, canonical_verb, embedding) VALUES ($1, $2, $3) RETURNING id",
			text, verb, pgvector.NewVector(emb)).Scan(&id))
		var sourceID int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO fact_sources (fact_id, source_url, source_type, published_at) VALUES ($1, $2, 'news', $3) RETURNING id",
			id, "https://example.test/"+text, published).Scan(&sourceID))
		if mention {
			_, err := storer.pool.Exec(ctx,
				"INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)", sourceID, pid)
			require.NoError(t, err)
		}
		return id
	}

	dA := date(1)
	dC := date(3)
	dD := date(4)
	a := insert("fact a", "CLAIMED", shifted(1, 0), &dA, true)
	b := insert("fact b", "DENIED", shifted(1, 0.1), nil, true)
	c := insert("fact c", "PROMISED", shifted(2, 0.2), &dC, true)
	// Two-step implication chain c -> d -> e: e is c's most general ancestor.
	// Neither ancestor is the person's fact.
	d := insert("fact d", "CLAIMED", shifted(3, 0.3), &dD, false)
	e := insert("fact e", "CLAIMED", shifted(3, 0.35), nil, false)
	for _, rel := range [][2]int64{{d, c}, {e, d}} {
		_, err := storer.pool.Exec(ctx,
			"INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'implication')",
			rel[0], rel[1])
		require.NoError(t, err)
	}
	// Nearest of all to a, but marked duplicate: excluded from both sides.
	dup := insert("fact dup", "CLAIMED", shifted(1, 0.05), nil, true)
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')", a, dup)
	require.NoError(t, err)
	// Past the L2 threshold: check-all only.
	far := insert("fact far", "OPPOSED", shifted(4, 5), nil, true)

	pairs, err := storer.GetNeighborPairsForPerson(ctx, pid, false)
	require.NoError(t, err)
	require.Len(t, pairs, 3, "a-b, a-c, b-c; dup and far excluded: %+v", pairs)

	assert.Equal(t, [2]int64{a, b}, [2]int64{pairs[0].FactID, pairs[0].NeighborID})
	assert.InDelta(t, 0.1, pairs[0].Distance, 1e-6)
	assert.Equal(t, "DENIED", pairs[0].NeighborVerb)
	assert.Equal(t, b, pairs[0].TargetID, "no ancestor: the neighbor is the target")
	assert.Equal(t, "fact b", pairs[0].TargetText)
	assert.Nil(t, pairs[0].TargetDate)

	assert.Equal(t, [2]int64{a, c}, [2]int64{pairs[1].FactID, pairs[1].NeighborID})
	assert.InDelta(t, 0.2, pairs[1].Distance, 1e-6)
	assert.Equal(t, "PROMISED", pairs[1].NeighborVerb, "the verb gate reads the neighbor, not the ancestor")
	assert.Equal(t, e, pairs[1].TargetID, "deepest implication ancestor")
	assert.Equal(t, "fact e", pairs[1].TargetText)
	assert.Nil(t, pairs[1].TargetDate, "the ancestor's own (missing) date replaces the neighbor's")

	assert.Equal(t, [2]int64{b, c}, [2]int64{pairs[2].FactID, pairs[2].NeighborID})
	assert.Equal(t, e, pairs[2].TargetID)

	// Ancestor date resolution: give e a source date and it must show up.
	_, err = storer.pool.Exec(ctx, "UPDATE fact_sources SET published_at = $1 WHERE fact_id = $2", date(5), e)
	require.NoError(t, err)
	pairs, err = storer.GetNeighborPairsForPerson(ctx, pid, false)
	require.NoError(t, err)
	require.Len(t, pairs, 3)
	require.NotNil(t, pairs[1].TargetDate)
	assert.True(t, pairs[1].TargetDate.Equal(date(5)))

	// Check-all: every active pair once, far included, no distance.
	all, err := storer.GetNeighborPairsForPerson(ctx, pid, true)
	require.NoError(t, err)
	got := make([][2]int64, 0, len(all))
	for _, p := range all {
		assert.Zero(t, p.Distance)
		got = append(got, [2]int64{p.FactID, p.TargetID})
	}
	assert.Equal(t, [][2]int64{{a, b}, {a, e}, {a, far}, {b, e}, {b, far}, {c, far}}, got)

	// The KNN cap applies before the id filter: with 31 closer facts below a,
	// nothing above a survives its 30 nearest.
	truncateFanoutTables(t, storer)
	pid2, _, err := storer.AddPerson(ctx, "Neighbor Subject Capped")
	require.NoError(t, err)
	pid = pid2
	for i := 0; i < 31; i++ {
		insert("close low", "CLAIMED", shifted(1, 0.01*float32(i+1)), nil, true)
	}
	anchor := insert("anchor", "CLAIMED", shifted(1, 0), nil, true)
	above := insert("above anchor", "DENIED", shifted(2, 0.5), nil, true)
	pairs, err = storer.GetNeighborPairsForPerson(ctx, pid, false)
	require.NoError(t, err)
	for _, p := range pairs {
		assert.False(t, p.FactID == anchor && p.NeighborID == above,
			"anchor's 30 nearest are all below it; %d-%d must not pair", anchor, above)
	}
	assert.NotEmpty(t, pairs, "the low facts still pair among themselves")
}
