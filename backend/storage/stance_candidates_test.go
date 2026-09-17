package storage

import (
	"context"
	"testing"

	"digital-mirror/backend/core"

	pgvector "github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// StanceCandidatesByIDs returns exactly the requested stances of the person,
// each with its nearest active member facts closest first, ordered by their
// nearest member fact; duplicate-marked members and other persons' stances
// are excluded, and an empty id list returns nothing.
func TestStanceCandidatesByIDs(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personID, _, err := storer.AddPerson(ctx, "candidate person")
	require.NoError(t, err)
	otherID, _, err := storer.AddPerson(ctx, "candidate other")
	require.NoError(t, err)

	fact := func(text string, emb []float32) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewVector(emb)).Scan(&id))
		return id
	}
	stance := func(person int64, label string, factIDs ...int64) int64 {
		id, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
			PersonID: person, ClusterLabel: label, ClusterEmbedding: mockEmbedding(), Summary: "s " + label,
			KeyFactIDs: factIDs, FactCount: len(factIDs),
		})
		require.NoError(t, err)
		for _, f := range factIDs {
			require.NoError(t, storer.AddFactToStance(ctx, id, f))
		}
		return id
	}
	query := makeUnitEmbedding(0)
	near := fact("near", makeMixedEmbedding(0, 1, 0.9, 0.1))
	far := fact("far", makeUnitEmbedding(5))
	dup := fact("dup near", makeMixedEmbedding(0, 1, 0.95, 0.05))
	nearest := fact("nearest", makeMixedEmbedding(0, 1, 0.99, 0.01))
	a := stance(personID, "topic a", far, near, dup)
	b := stance(personID, "topic b", nearest)
	c := stance(personID, "topic c", fact("elsewhere", makeUnitEmbedding(7)))
	foreign := stance(otherID, "not mine", fact("other person", makeUnitEmbedding(0)))
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')", near, dup)
	require.NoError(t, err)

	got, err := storer.StanceCandidatesByIDs(ctx, personID, query, []int64{a, b, foreign}, 6)
	require.NoError(t, err)
	require.Len(t, got, 2, "only the person's own requested stances")
	require.Equal(t, b, got[0].ID, "ordered by nearest member fact")
	require.Equal(t, a, got[1].ID)
	require.Equal(t, []string{"near", "far"}, got[1].MemberFacts, "closest first, duplicate member excluded")
	require.Equal(t, "topic a", got[1].Label)
	require.Equal(t, 3, got[1].FactCount)
	_ = c

	none, err := storer.StanceCandidatesByIDs(ctx, personID, query, nil, 6)
	require.NoError(t, err)
	require.Empty(t, none)
}
