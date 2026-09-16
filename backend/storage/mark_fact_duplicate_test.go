package storage

import (
	"context"
	"digital-mirror/backend/core"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMarkFactDuplicate verifies the contradiction-pass dedup write: it inserts
// a 'duplicate' fact_relations row keeping the lower id as canonical, is
// idempotent, rejects self-relations, and hides the duplicate from active-fact
// queries.
func TestMarkFactDuplicate(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, err := storer.FindOrInsertPersonWithType(ctx, "dup test subject", "human")
	require.NoError(t, err)

	faID, _, err := insertFactForTest(ctx, t, storer, core.StoreFactInput{
		SubjectEntity: "dup test subject", SubjectEntityType: "human", Lang: "en",
		NormalizedFactText: "subject will see his candidacy through to the end", EmbeddingText: "candidacy",
		Embedding: mockEmbedding(), CanonicalVerb: "PROMISED", SourceURL: "http://example.com/a", SourceType: "web",
	})
	require.NoError(t, err)
	fbID, _, err := insertFactForTest(ctx, t, storer, core.StoreFactInput{
		SubjectEntity: "dup test subject", SubjectEntityType: "human", Lang: "en",
		NormalizedFactText: "subject's candidacy would go all the way", EmbeddingText: "candidacy",
		Embedding: mockEmbedding(), CanonicalVerb: "CLAIMED", SourceURL: "http://example.com/b", SourceType: "web",
	})
	require.NoError(t, err)

	general, specific := faID, fbID
	if general > specific {
		general, specific = specific, general
	}

	// Self-relation is rejected.
	require.Error(t, storer.MarkFactDuplicate(ctx, faID, faID, "self"))

	// Mark duplicate (pass ids in either order — it canonicalizes to lower=general).
	require.NoError(t, storer.MarkFactDuplicate(ctx, specific, general, "same claim"))

	var gotGeneral, gotSpecific int64
	var relType string
	err = storer.pool.QueryRow(ctx,
		`SELECT general_fact_id, specific_fact_id, relation_type FROM fact_relations
		 WHERE relation_type='duplicate' AND general_fact_id=$1 AND specific_fact_id=$2`,
		general, specific).Scan(&gotGeneral, &gotSpecific, &relType)
	require.NoError(t, err, "expected a duplicate relation row with lower id as general")
	assert.Equal(t, general, gotGeneral)
	assert.Equal(t, specific, gotSpecific)
	assert.Equal(t, "duplicate", relType)

	// Idempotent: second call adds no second row.
	require.NoError(t, storer.MarkFactDuplicate(ctx, general, specific, "again"))
	var n int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT count(*) FROM fact_relations WHERE general_fact_id=$1 AND specific_fact_id=$2`,
		general, specific).Scan(&n))
	assert.Equal(t, 1, n, "MarkFactDuplicate must be idempotent")

	// The specific (duplicate) fact is now excluded from active-fact similarity search.
	similar, err := storer.FindSimilarActiveFacts(ctx, []int64{pid}, mockEmbedding(), 0.9, 10)
	require.NoError(t, err)
	for _, sf := range similar {
		assert.NotEqual(t, specific, sf.ID, "duplicate fact must be excluded from active facts")
	}
}
