package storage

import (
	"context"
	"digital-mirror/backend/core"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRepointFactDuplicate exercises all three branches: repoint a contradiction
// onto the canonical fact, drop one that becomes redundant, and drop the
// fact-vs-its-own-duplicate self-pair.
func TestRepointFactDuplicate(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, err := storer.FindOrInsertPersonWithType(ctx, "repoint subject", "human")
	require.NoError(t, err)

	mk := func(text, verb, url string) int64 {
		id, _, e := insertFactForTest(ctx, t, storer, core.StoreFactInput{PublishedAt: &storageTestDate,
			SubjectEntity: "repoint subject", SubjectEntityType: "human", Lang: "en",
			NormalizedFactText: text, EmbeddingText: text, Embedding: mockEmbedding(),
			CanonicalVerb: verb, SourceURL: url, SourceType: "web",
		})
		require.NoError(t, e)
		require.NotZero(t, id, "fact %q must insert (distinct verb avoids auto-dedup)", text)
		return id
	}
	// Distinct verbs so the insert-time verb gate doesn't dedup the mock embeddings.
	a := mk("canonical fact A", "PROMISED", "http://e/a")
	b := mk("duplicate fact B", "CLAIMED", "http://e/b")
	x := mk("other fact X", "SUPPORTED", "http://e/x")
	y := mk("other fact Y", "OPPOSED", "http://e/y")

	ins := func(f1, f2 int64) {
		lo, hi := f1, f2
		if lo > hi {
			lo, hi = hi, lo
		}
		_, e := storer.pool.Exec(ctx,
			`INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason) VALUES ($1,$2,$3,'t')`, pid, lo, hi)
		require.NoError(t, e)
	}
	ins(b, x) // → should repoint to (A,X)
	ins(a, y) // pre-existing canonical pair
	ins(b, y) // → redundant with (A,Y) → dropped
	ins(a, b) // → self-pair (A vs its own duplicate) → dropped

	// The raw relation only: this test drives RepointFactDuplicate itself.
	general, specific := a, b
	if general > specific {
		general, specific = specific, general
	}
	_, err = storer.pool.Exec(ctx,
		"INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')", general, specific)
	require.NoError(t, err)

	repointed, dropped, err := storer.RepointFactDuplicate(ctx, general, specific)
	require.NoError(t, err)
	assert.Equal(t, 1, repointed, "(B,X) repointed to (A,X)")
	assert.Equal(t, 2, dropped, "(B,Y) redundant + (A,B) self-pair dropped")

	// No contradiction may still reference the hidden specific fact.
	var refs int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT count(*) FROM contradictions WHERE fact_a_id=$1 OR fact_b_id=$1`, specific).Scan(&refs))
	assert.Equal(t, 0, refs, "no contradiction should reference the duplicate fact after repoint")

	// Exactly (A,X) and (A,Y) remain for the person.
	var total int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT count(*) FROM contradictions WHERE person_id=$1`, pid).Scan(&total))
	assert.Equal(t, 2, total)

	// (general,X) now exists (the repointed row).
	loGX, hiGX := general, x
	if loGX > hiGX {
		loGX, hiGX = hiGX, loGX
	}
	var hasGX bool
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM contradictions WHERE person_id=$1 AND fact_a_id=$2 AND fact_b_id=$3)`,
		pid, loGX, hiGX).Scan(&hasGX))
	assert.True(t, hasGX, "(general, X) must exist after repoint")
}
