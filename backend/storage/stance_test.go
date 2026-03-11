package storage

import (
	"context"
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
	doc := StanceDocument{
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
	err = storer.UpdateStanceDocument(ctx, stanceID, "Updated summary.", newEmb, []int64{1, 2, 3, 4}, 4)
	require.NoError(t, err)

	// Verify update
	stances, err = storer.GetStanceDocumentsForPerson(ctx, personID)
	require.NoError(t, err)
	require.Len(t, stances, 1)
	assert.Equal(t, "Updated summary.", stances[0].Summary)
	assert.Equal(t, 4, stances[0].FactCount)
	assert.Equal(t, []int64{1, 2, 3, 4}, stances[0].KeyFactIDs)

	// Counts
	totalStances, totalFacts, err := storer.GetStanceDocumentCounts(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, totalStances)
	assert.Equal(t, 4, totalFacts)

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

	_, err = storer.InsertStanceDocument(ctx, StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "topic-a",
		ClusterEmbedding: emb1,
		Summary:          "Topic A summary",
		KeyFactIDs:       []int64{10},
		FactCount:        1,
	})
	require.NoError(t, err)

	_, err = storer.InsertStanceDocument(ctx, StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "topic-b",
		ClusterEmbedding: emb2,
		Summary:          "Topic B summary",
		KeyFactIDs:       []int64{20},
		FactCount:        1,
	})
	require.NoError(t, err)

	// Search with embedding close to emb1 — should find topic-a
	queryEmb := makeMixedEmbedding(0, 2, 0.95, 0.05) // close to [1,0,0...] direction
	match, err := storer.FindMatchingStance(ctx, personID, queryEmb, 0.35)
	require.NoError(t, err)
	require.NotNil(t, match, "expected to find a matching stance")
	assert.Equal(t, "topic-a", match.ClusterLabel)
	assert.Less(t, match.Distance, 0.35)

	// Search with embedding far from both — should return nil
	farEmb := makeUnitEmbedding(500) // orthogonal to both
	match, err = storer.FindMatchingStance(ctx, personID, farEmb, 0.35)
	require.NoError(t, err)
	assert.Nil(t, match, "expected no match for distant embedding")

	// Search with wrong person — should return nil
	match, err = storer.FindMatchingStance(ctx, personID+999, queryEmb, 0.35)
	require.NoError(t, err)
	assert.Nil(t, match, "expected no match for wrong person")
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
		100, "Test fact text", pgvector.NewVector(makeUnitEmbedding(0)))
	require.NoError(t, err)

	_, err = storer.pool.Exec(ctx, `INSERT INTO facts (id, fact_text, embedding, canonical_verb, embedding_text) VALUES ($1, $2, $3, $4, $5)`,
		101, "Second fact text", pgvector.NewVector(makeUnitEmbedding(1)), "DENIED", "wrongdoing case")
	require.NoError(t, err)

	// Insert stance
	stanceID, err := storer.InsertStanceDocument(ctx, StanceDocument{
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
	_, err = storer.InsertStanceDocument(ctx, StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "unique-topic",
		ClusterEmbedding: emb,
		Summary:          "First",
		KeyFactIDs:       []int64{},
		FactCount:        1,
	})
	require.NoError(t, err)

	// Insert second stance with same person+label should fail
	_, err = storer.InsertStanceDocument(ctx, StanceDocument{
		PersonID:         personID,
		ClusterLabel:     "unique-topic",
		ClusterEmbedding: emb,
		Summary:          "Duplicate",
		KeyFactIDs:       []int64{},
		FactCount:        1,
	})
	assert.Error(t, err, "expected unique constraint violation")
}
