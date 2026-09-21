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

	got, err := storer.StanceCandidatesByIDs(ctx, personID, query, []int64{a, b, foreign}, 6, 0)
	require.NoError(t, err)
	require.Len(t, got, 2, "only the person's own requested stances")
	require.Equal(t, b, got[0].ID, "ordered by nearest member fact")
	require.Equal(t, a, got[1].ID)
	require.Equal(t, []string{"near", "far"}, got[1].MemberFacts, "closest first, duplicate member excluded")
	require.Equal(t, "topic a", got[1].Label)
	require.Equal(t, 3, got[1].FactCount)
	_ = c

	none, err := storer.StanceCandidatesByIDs(ctx, personID, query, nil, 6, 0)
	require.NoError(t, err)
	require.Empty(t, none)
}

// A fact hidden from the evidence never appears as a candidate's member fact,
// so its own stance is ranked on its other members; and the sampler only
// draws active facts from multi-fact stances of the given persons.
func TestShortlistExcludesFactAndSamplesMultiFactStances(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personID, _, err := storer.AddPerson(ctx, "shortlist exclude person")
	require.NoError(t, err)
	fact := func(text string, emb []float32) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewVector(emb)).Scan(&id))
		return id
	}
	stance := func(label string, factIDs ...int64) int64 {
		id, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
			PersonID: personID, ClusterLabel: label, ClusterEmbedding: mockEmbedding(), Summary: "s " + label,
			KeyFactIDs: factIDs, FactCount: len(factIDs),
		})
		require.NoError(t, err)
		for _, f := range factIDs {
			require.NoError(t, storer.AddFactToStance(ctx, id, f))
		}
		return id
	}
	query := makeUnitEmbedding(0)
	self := fact("the fact itself", query)
	sibling := fact("its sibling", makeMixedEmbedding(0, 1, 0.9, 0.1))
	lone := fact("a lone fact", makeUnitEmbedding(5))
	pair := stance("pair", self, sibling)
	stance("single", lone)

	got, err := storer.ShortlistStancesByNearestFact(ctx, personID, query, 8, 6, self)
	require.NoError(t, err)
	require.Equal(t, pair, got[0].ID)
	require.Equal(t, []string{"its sibling"}, got[0].MemberFacts, "the hidden fact must not be evidence")

	sample, err := storer.SampleFactsInMultiFactStances(ctx, []int64{personID}, 10)
	require.NoError(t, err)
	ids := []int64{}
	for _, s := range sample {
		require.Equal(t, pair, s.StanceID)
		require.Equal(t, "pair", s.StanceLabel)
		ids = append(ids, s.FactID)
	}
	require.ElementsMatch(t, []int64{self, sibling}, ids, "only the multi-fact stance's facts")
	none, err := storer.SampleFactsInMultiFactStances(ctx, []int64{personID + 1000}, 10)
	require.NoError(t, err)
	require.Empty(t, none)
}

// ListLabelSimilarStancePairs surfaces one person's label-similar pairs once,
// as [smaller, larger], and never crosses persons.
func TestListLabelSimilarStancePairs(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE TABLE stance_document_facts, stance_documents RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	personID, _, err := storer.AddPerson(ctx, "label pairs person")
	require.NoError(t, err)
	otherID, _, err := storer.AddPerson(ctx, "label pairs other")
	require.NoError(t, err)
	stance := func(person int64, label string) int64 {
		id, err := storer.InsertStanceDocument(ctx, core.StanceDocument{
			PersonID: person, ClusterLabel: label, ClusterEmbedding: mockEmbedding(), Summary: "s " + label, KeyFactIDs: []int64{},
		})
		require.NoError(t, err)
		return id
	}
	a := stance(personID, "politique environnementale et régulations écologiques")
	b := stance(personID, "Politique environnementale et Pacte vert")
	stance(personID, "réforme des retraites")
	stance(otherID, "politique environnementale et transition")

	pairs, err := storer.ListLabelSimilarStancePairs(ctx, personID, 0.40)
	require.NoError(t, err)
	require.Equal(t, [][2]int64{{a, b}}, pairs)
}
