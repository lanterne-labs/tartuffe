package storage

import (
	"fmt"
	"context"
	"digital-mirror/backend/core"
	"math"
	"testing"

	pgvector "github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeUnitEmbedding creates a unit embedding pointing in a specific direction.
// index specifies which dimension gets value 1.0; all others are 0.
func makeUnitEmbedding(index int) []float32 {
	emb := make([]float32, embeddingDimension)
	emb[index] = 1.0
	return emb
}

// makeMixedEmbedding creates an embedding that's a weighted blend of two unit vectors, then normalized.
func makeMixedEmbedding(i, j int, weightI, weightJ float64) []float32 {
	emb := make([]float32, embeddingDimension)
	emb[i] = float32(weightI)
	emb[j] = float32(weightJ)
	norm := math.Sqrt(weightI*weightI + weightJ*weightJ)
	emb[i] = float32(weightI / norm)
	emb[j] = float32(weightJ / norm)
	return emb
}

func TestStanceDocumentCRUD(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()

	ctx := context.Background()

	// Clean stance tables
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	// Create a person
	personID, _, err := storer.AddPerson(ctx, "Test Person")
	require.NoError(t, err)

	emb := makeUnitEmbedding(0)

	// Insert a stance document
	doc := core.StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "test-topic",
		ClusterEmbedding: emb,
		Summary:          "Test person denied wrongdoing.",
		KeyFactIDs:       []int64{1, 2, 3},
		FactCount:        3,
	}
	stanceID, err := storer.InsertStanceDocument(ctx, doc)
	require.NoError(t, err)
	assert.Greater(t, stanceID, int64(0))

	// Get stances for person
	stances, err := storer.GetStanceDocumentsForPerson(ctx, personID)
	require.NoError(t, err)
	require.Len(t, stances, 1)
	assert.Equal(t, "test-topic", stances[0].ClusterLabel)
	assert.Equal(t, "Test person denied wrongdoing.", stances[0].Summary)
	assert.Equal(t, 3, stances[0].FactCount)
	assert.Equal(t, []int64{1, 2, 3}, stances[0].KeyFactIDs)

	// Update the stance
	newEmb := makeMixedEmbedding(0, 1, 0.9, 0.1)
	err = storer.UpdateStanceDocument(ctx, stanceID, "Updated summary.", "", newEmb, []int64{1, 2, 3, 4})
	require.NoError(t, err)

	// Verify update
	stances, err = storer.GetStanceDocumentsForPerson(ctx, personID)
	require.NoError(t, err)
	require.Len(t, stances, 1)
	assert.Equal(t, "Updated summary.", stances[0].Summary)
	assert.Equal(t, 3, stances[0].FactCount, "the update leaves the count to the stance's rows")
	assert.Equal(t, []int64{1, 2, 3, 4}, stances[0].KeyFactIDs)

	// The count follows the rows: two facts join, one leaves.
	f1 := insertDatedFact(t, storer, personID, "Test Person a voté pour le texte A.", nil)
	f2 := insertDatedFact(t, storer, personID, "Test Person a voté contre le texte B.", nil)
	require.NoError(t, storer.AddFactToStance(ctx, stanceID, f1))
	require.NoError(t, storer.AddFactToStance(ctx, stanceID, f2))
	require.NoError(t, storer.AddFactToStance(ctx, stanceID, f2), "a fact already held is not counted twice")
	stances, err = storer.GetStanceDocumentsForPerson(ctx, personID)
	require.NoError(t, err)
	assert.Equal(t, 2, stances[0].FactCount)
	_, err = storer.pool.Exec(ctx, "DELETE FROM stance_document_facts WHERE fact_id = $1", f1)
	require.NoError(t, err)
	stances, err = storer.GetStanceDocumentsForPerson(ctx, personID)
	require.NoError(t, err)
	assert.Equal(t, 1, stances[0].FactCount)

	// Counts
	totalStances, totalFacts, err := storer.GetStanceDocumentCounts(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, totalStances)
	assert.Equal(t, 1, totalFacts)

	// Delete
	err = storer.DeleteStanceDocumentsForPerson(ctx, personID)
	require.NoError(t, err)

	stances, err = storer.GetStanceDocumentsForPerson(ctx, personID)
	require.NoError(t, err)
	assert.Empty(t, stances)
}

func TestStanceDocumentFindMatching(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()

	ctx := context.Background()

	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personID, _, err := storer.AddPerson(ctx, "Test Person 2")
	require.NoError(t, err)

	// Insert two stances with very different embeddings
	emb1 := makeUnitEmbedding(0) // [1,0,0,0,...]
	emb2 := makeUnitEmbedding(1) // [0,1,0,0,...]

	_, err = storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "topic-a",
		ClusterEmbedding: emb1,
		Summary:          "Topic A summary",
		KeyFactIDs:       []int64{10},
		FactCount:        1,
	})
	require.NoError(t, err)

	_, err = storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "topic-b",
		ClusterEmbedding: emb2,
		Summary:          "Topic B summary",
		KeyFactIDs:       []int64{20},
		FactCount:        1,
	})
	require.NoError(t, err)

}

func TestStanceDocumentFactsJoinTable(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()

	ctx := context.Background()

	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personID, _, err := storer.AddPerson(ctx, "Test Person 3")
	require.NoError(t, err)

	// Insert a fact — must use pgvector.NewVector for the embedding parameter
	_, err = storer.pool.Exec(ctx, `INSERT INTO facts (id, fact_text, embedding) VALUES ($1, $2, $3)`,
		100, "Test fact text", pgvector.NewHalfVector(makeUnitEmbedding(0)))
	require.NoError(t, err)

	_, err = storer.pool.Exec(ctx, `INSERT INTO facts (id, fact_text, embedding, canonical_verb, embedding_text) VALUES ($1, $2, $3, $4, $5)`,
		101, "Second fact text", pgvector.NewHalfVector(makeUnitEmbedding(1)), "DENIED", "wrongdoing case")
	require.NoError(t, err)

	// Insert stance
	stanceID, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "test-join",
		ClusterEmbedding: makeUnitEmbedding(0),
		Summary:          "Join test",
		KeyFactIDs:       []int64{100, 101},
		FactCount:        2,
	})
	require.NoError(t, err)

	// Add facts to stance
	err = storer.AddFactToStance(ctx, stanceID, 100)
	require.NoError(t, err)
	err = storer.AddFactToStance(ctx, stanceID, 101)
	require.NoError(t, err)

	// Idempotent — adding same fact again should not error
	err = storer.AddFactToStance(ctx, stanceID, 100)
	require.NoError(t, err)

	// Get facts for stance
	facts, err := storer.GetFactsForStance(ctx, stanceID)
	require.NoError(t, err)
	require.Len(t, facts, 2)

	// Verify fact details
	factIDs := map[int64]bool{}
	for _, f := range facts {
		factIDs[f.ID] = true
	}
	assert.True(t, factIDs[100], "expected fact 100")
	assert.True(t, factIDs[101], "expected fact 101")

	// Check that fact 101 has verb and embedding_text
	for _, f := range facts {
		if f.ID == 101 {
			assert.Equal(t, "DENIED", f.CanonicalVerb)
			assert.Equal(t, "wrongdoing case", f.EmbeddingText)
		}
	}
}

// TestMergeStancesForPerson covers the transactional merge path used by the
// per-person centroid post-pass: facts move (with dedup), contradictions
// re-pin BEFORE the src row is dropped, dst's centroid+fact_count are rewritten,
// and the src row disappears.
func TestMergeStancesForPerson(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()

	ctx := context.Background()

	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents, contradictions, facts RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personID, _, err := storer.AddPerson(ctx, "Merge Test Person")
	require.NoError(t, err)

	// Two facts that will live on src and dst respectively, plus a third already
	// on dst so we can verify ON CONFLICT doesn't choke when src has the same
	// fact attached (we add fact 200 to both stances).
	_, err = storer.pool.Exec(ctx, `INSERT INTO facts (id, fact_text, embedding) VALUES ($1, $2, $3)`,
		200, "Fact on both src and dst", pgvector.NewHalfVector(makeUnitEmbedding(0)))
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx, `INSERT INTO facts (id, fact_text, embedding) VALUES ($1, $2, $3)`,
		201, "Fact only on src", pgvector.NewHalfVector(makeUnitEmbedding(1)))
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx, `INSERT INTO facts (id, fact_text, embedding) VALUES ($1, $2, $3)`,
		202, "Fact only on dst", pgvector.NewHalfVector(makeUnitEmbedding(2)))
	require.NoError(t, err)

	srcID, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "src-stance",
		ClusterEmbedding: makeUnitEmbedding(0),
		Summary:          "Source",
		KeyFactIDs:       []int64{200, 201},
		FactCount:        2,
	})
	require.NoError(t, err)
	dstID, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "dst-stance",
		ClusterEmbedding: makeUnitEmbedding(1),
		Summary:          "Destination",
		KeyFactIDs:       []int64{200, 202},
		FactCount:        2,
	})
	require.NoError(t, err)

	require.NoError(t, storer.AddFactToStance(ctx, srcID, 200))
	require.NoError(t, storer.AddFactToStance(ctx, srcID, 201))
	require.NoError(t, storer.AddFactToStance(ctx, dstID, 200))
	require.NoError(t, storer.AddFactToStance(ctx, dstID, 202))

	// A contradiction whose bundle is currently src — the merge must repoint it
	// to dst. We bypass RecordContradiction's tx requirement by inserting raw.
	var cid int64
	err = storer.pool.QueryRow(ctx, `
		INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason, bundle_stance_id)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		personID, int64(200), int64(201), "test contradiction", srcID).Scan(&cid)
	require.NoError(t, err)

	newCentroid := makeMixedEmbedding(0, 1, 0.6, 0.4)
	require.NoError(t, storer.MergeStancesForPerson(ctx, srcID, dstID, newCentroid))

	// src row gone
	stances, err := storer.GetStanceDocumentsForPerson(ctx, personID)
	require.NoError(t, err)
	require.Len(t, stances, 1)
	assert.Equal(t, dstID, stances[0].ID)
	assert.Equal(t, 3, stances[0].FactCount, "dst should now have 3 unique facts (200,201,202)")
	assert.InDeltaSlice(t, newCentroid, stances[0].ClusterEmbedding, 1e-3) // halfvec precision

	// All three facts on dst
	facts, err := storer.GetFactsForStance(ctx, dstID)
	require.NoError(t, err)
	gotIDs := map[int64]bool{}
	for _, f := range facts {
		gotIDs[f.ID] = true
	}
	assert.True(t, gotIDs[200])
	assert.True(t, gotIDs[201])
	assert.True(t, gotIDs[202])

	// src has nothing left
	srcFacts, err := storer.GetFactsForStance(ctx, srcID)
	require.NoError(t, err)
	assert.Empty(t, srcFacts)

	// Contradiction now bundles dst
	var bundle *int64
	err = storer.pool.QueryRow(ctx, `SELECT bundle_stance_id FROM contradictions WHERE id = $1`, cid).Scan(&bundle)
	require.NoError(t, err)
	require.NotNil(t, bundle)
	assert.Equal(t, dstID, *bundle)
}

// TestUpdateStanceSummary covers the storer hook used by the LLM-merge phase:
// summary + cluster_label are rewritten, centroid/fact_count/key_fact_ids are
// preserved, and last_updated_at advances. Empty summary or label is rejected.
func TestUpdateStanceSummary(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()

	ctx := context.Background()

	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personID, _, err := storer.AddPerson(ctx, "Update Summary Test Person")
	require.NoError(t, err)

	emb := makeUnitEmbedding(0)
	stanceID, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "old-label",
		ClusterEmbedding: emb,
		Summary:          "Old summary.",
		KeyFactIDs:       []int64{42, 43},
		FactCount:        2,
	})
	require.NoError(t, err)

	require.NoError(t, storer.UpdateStanceSummary(ctx, stanceID, "Unified summary covering both stances.", "merged-label"))

	stances, err := storer.GetStanceDocumentsForPerson(ctx, personID)
	require.NoError(t, err)
	require.Len(t, stances, 1)
	got := stances[0]
	assert.Equal(t, "Unified summary covering both stances.", got.Summary)
	assert.Equal(t, "merged-label", got.ClusterLabel)
	// Untouched fields:
	assert.Equal(t, []int64{42, 43}, got.KeyFactIDs)
	assert.Equal(t, 2, got.FactCount)
	assert.InDeltaSlice(t, emb, got.ClusterEmbedding, 1e-5)

	// Empty summary / label should be rejected.
	assert.Error(t, storer.UpdateStanceSummary(ctx, stanceID, "", "label"))
	assert.Error(t, storer.UpdateStanceSummary(ctx, stanceID, "   ", "label"))
	assert.Error(t, storer.UpdateStanceSummary(ctx, stanceID, "summary", ""))
	assert.Error(t, storer.UpdateStanceSummary(ctx, stanceID, "summary", "   "))
}

func TestStanceDocumentUniqueConstraint(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()

	ctx := context.Background()

	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personID, _, err := storer.AddPerson(ctx, "Test Person 4")
	require.NoError(t, err)

	emb := makeUnitEmbedding(0)

	// Insert first stance
	firstID, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "unique-topic",
		ClusterEmbedding: emb,
		Summary:          "First",
		KeyFactIDs:       []int64{},
		FactCount:        1,
	})
	require.NoError(t, err)

	// A colliding (person, label) insert resolves to the EXISTING stance
	// instead of erroring: the topic assigner can emit NEW_STANCE with a label
	// the person already has, and failing here used to abort whole insert
	// batches (frpolitics backfill 2026-09-04). The existing row's content is
	// untouched — the metadata refresh reconciles it once facts attach.
	secondID, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "unique-topic",
		ClusterEmbedding: emb,
		Summary:          "Duplicate",
		KeyFactIDs:       []int64{},
		FactCount:        1,
	})
	require.NoError(t, err, "label collision must resolve, not error")
	require.Equal(t, firstID, secondID, "collision must return the existing stance id")

	var summary string
	require.NoError(t, storer.pool.QueryRow(ctx, "SELECT summary FROM stance_documents WHERE id=$1", firstID).Scan(&summary))
	require.Equal(t, "First", summary, "existing stance content must not be clobbered")
}

func TestGetClosestFactsForStance(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	personID, _, err := storer.AddPerson(ctx, "Test Person Closest")
	require.NoError(t, err)

	stanceID, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
		PersonID: personID, ClusterLabel: "test-closest", ClusterEmbedding: makeUnitEmbedding(0), Summary: "Closest test",
	})
	require.NoError(t, err)
	// 200 is the nearest, 201 next, 202 far; 203 is as near as 200 but a
	// grouped duplicate of it.
	for id, emb := range map[int64][]float32{
		200: makeUnitEmbedding(0),
		201: makeMixedEmbedding(0, 1, 0.8, 0.6),
		202: makeUnitEmbedding(1),
		203: makeUnitEmbedding(0),
	} {
		_, err = storer.pool.Exec(ctx, `INSERT INTO facts (id, fact_text, embedding) VALUES ($1, $2, $3)`,
			id, fmt.Sprintf("fact %d", id), pgvector.NewHalfVector(emb))
		require.NoError(t, err)
		require.NoError(t, storer.AddFactToStance(ctx, stanceID, id))
	}
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES (200, 203, 'duplicate')")
	require.NoError(t, err)

	facts, err := storer.GetClosestFactsForStance(ctx, stanceID, makeUnitEmbedding(0), 2)
	require.NoError(t, err)
	require.Len(t, facts, 2)
	require.Equal(t, int64(200), facts[0].ID)
	require.Equal(t, int64(201), facts[1].ID)
}
