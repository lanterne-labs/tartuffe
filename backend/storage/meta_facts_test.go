package storage

import (
	"context"
	"digital-mirror/backend/core"
	"fmt"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Meta-fact lifecycle: creation links the meta to the person, members resolve
// to it, member marking removes members from the deduped fact count, and a text
// update re-opens the meta's pair checks.
func TestMetaFactLifecycle(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('meta fact person', 'human') RETURNING id`,
	).Scan(&personID))

	insertFact := func(text string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id`,
			text, pgvector.NewVector(mockEmbedding()),
		).Scan(&id))
		_, err := storer.pool.Exec(ctx,
			`INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type) VALUES ($1, $2, $3, 'test')`,
			id, fmt.Sprintf("http://ex/%d", id), text)
		require.NoError(t, err)
		_, err = storer.pool.Exec(ctx,
			`INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $2 FROM fact_sources WHERE fact_id = $1 ORDER BY id DESC LIMIT 1`,
			id, personID)
		require.NoError(t, err)
		return id
	}

	fa := insertFact("A: affirme son innocence")
	fb := insertFact("B: clame son innocence")

	metaID, err := storer.CreateMetaFact(ctx, personID, "M: affirme son innocence (agrégé)", "innocence", mockEmbedding(), "fr", "affirmer")
	require.NoError(t, err)

	// The meta is person-linked and flagged is_meta.
	isMeta, err := storer.IsMetaFact(ctx, metaID)
	require.NoError(t, err)
	assert.True(t, isMeta)

	// Members resolve to the meta once marked; the meta resolves to itself.
	require.NoError(t, storer.MarkFactAsDuplicate(ctx, fa, metaID))
	require.NoError(t, storer.MarkFactAsDuplicate(ctx, fb, metaID))
	gotMeta, found, err := storer.GetMetaForFact(ctx, fa)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, metaID, gotMeta)
	gotMeta, found, err = storer.GetMetaForFact(ctx, metaID)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, metaID, gotMeta)

	texts, err := storer.GetMetaMemberTexts(ctx, metaID)
	require.NoError(t, err)
	assert.Equal(t, []string{"A: affirme son innocence", "B: clame son innocence"}, texts)

	// Deduped fact count: 2 members fold away, the meta counts once → 1.
	require.NoError(t, storer.RefreshPersonScore(ctx, personID))
	var factCount int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT fact_count FROM persons WHERE id = $1`, personID).Scan(&factCount))
	assert.Equal(t, 1, factCount, "members must count via the meta only")

	// A pair check involving the meta is cleared by a text update (re-opened).
	other := insertFact("C: fait divers sans rapport")
	a, b := metaID, other
	if a > b {
		a, b = b, a
	}
	_, err = storer.pool.Exec(ctx,
		`INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction) VALUES ($1, $2, $3, false)`,
		personID, a, b)
	require.NoError(t, err)

	require.NoError(t, storer.UpdateMetaFactText(ctx, metaID, "M: texte agrégé mis à jour"))
	var checks int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM fact_pair_checks WHERE fact_a_id = $1 OR fact_b_id = $1`, metaID).Scan(&checks))
	assert.Equal(t, 0, checks, "meta text update must re-open its pair checks")

	// Updating a non-meta fact through the meta API must fail loudly.
	assert.Error(t, storer.UpdateMetaFactText(ctx, other, "nope"))
}

// The review's storage: metas listed from a date with their person, members
// with texts, and unfolding a member makes it active again (and refuses a
// non-member).
func TestMetaReviewStorage(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, _, err := storer.AddPerson(ctx, "meta review person")
	require.NoError(t, err)
	member := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewVector(mockEmbedding())).Scan(&id))
		return id
	}
	a := member("review member a")
	b := member("review member b")
	metaID, err := storer.CreateMetaFact(ctx, pid, "review meta", "slug", mockEmbedding(), "fr", "")
	require.NoError(t, err)
	require.NoError(t, storer.MarkFactAsDuplicate(ctx, a, metaID))
	require.NoError(t, storer.MarkFactAsDuplicate(ctx, b, metaID))

	metas, err := storer.ListMetaFacts(ctx, time.Now().Add(-time.Minute), nil)
	require.NoError(t, err)
	require.Len(t, metas, 1)
	require.Equal(t, core.MetaFactRef{ID: metaID, PersonID: pid, PersonName: "meta review person", Text: "review meta"}, metas[0])
	later, err := storer.ListMetaFacts(ctx, time.Now().Add(time.Minute), nil)
	require.NoError(t, err)
	require.Empty(t, later)
	only, err := storer.ListMetaFacts(ctx, time.Now().Add(-time.Minute), []int64{metaID + 1000})
	require.NoError(t, err)
	require.Empty(t, only, "an id filter that matches nothing lists nothing")
	only, err = storer.ListMetaFacts(ctx, time.Now().Add(-time.Minute), []int64{metaID})
	require.NoError(t, err)
	require.Len(t, only, 1)

	members, err := storer.GetMetaMembers(ctx, metaID)
	require.NoError(t, err)
	require.Equal(t, []core.FactForFold{{ID: a, Text: "review member a"}, {ID: b, Text: "review member b"}}, members)

	require.NoError(t, storer.UnfoldFactFromMeta(ctx, metaID, b))
	f, err := storer.GetFactByID(ctx, b)
	require.NoError(t, err)
	require.True(t, f.IsCanonical, "an unfolded member is active again")
	require.Error(t, storer.UnfoldFactFromMeta(ctx, metaID, b), "unfolding a non-member is an error")
}
