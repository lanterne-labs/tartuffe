package storage

import (
	"context"
	"testing"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The news pages of a candidate's facts whose text is not stored are listed
// to be read again; a page stored, or one whose refetch failed, is not
// listed again, and the document of a refetched page is not one the worker
// extracts.
func TestRefetchArticles(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, attributes) VALUES ('refetch person', 'human', '{"candidate": true}') RETURNING id`,
	).Scan(&personID))
	addFact := func(url string) {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ('a fact of '||$1, $2) RETURNING id`,
			url, pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		_, err := storer.pool.Exec(ctx, `
			WITH s AS (INSERT INTO fact_sources (fact_id, source_url, source_type) VALUES ($1, $2, 'news') RETURNING id)
			INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $3 FROM s`, id, url, personID)
		require.NoError(t, err)
	}
	const lost, never, kept, gone = "http://refetch.test/lost", "http://refetch.test/never", "http://refetch.test/kept", "http://refetch.test/gone"
	for _, u := range []string{lost, never, kept, gone} {
		addFact(u)
	}
	// lost: extracted before the text was kept; kept: its text is there; never: no document.
	_, err := storer.pool.Exec(ctx, `
		INSERT INTO source_documents (source_type, url, title, content, state, extracted_at) VALUES
		('news', $1, 'the lost page', '', 'extracted', now()), ('news', $2, 'the kept page', 'its text', 'extracted', now())`, lost, kept)
	require.NoError(t, err)

	listed := func() []string {
		t.Helper()
		urls, err := storer.ListNewsURLsWithoutText(ctx, true, 1<<20)
		require.NoError(t, err)
		var mine []string
		for _, u := range urls {
			if u == lost || u == never || u == kept || u == gone {
				mine = append(mine, u)
			}
		}
		return mine
	}
	assert.ElementsMatch(t, []string{lost, never, gone}, listed())

	require.NoError(t, storer.StoreRefetchedArticle(ctx, lost, "another title", "the text read again"))
	require.NoError(t, storer.StoreRefetchedArticle(ctx, never, "a title", "a text"))
	require.NoError(t, storer.MarkRefetchFailed(ctx, gone, "404"))
	assert.Empty(t, listed())

	var title, content, state string
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT title, content, state FROM source_documents WHERE url = $1`, lost).Scan(&title, &content, &state))
	assert.Equal(t, "the lost page", title, "the stored title stands")
	assert.Equal(t, "the text read again", content)
	assert.Equal(t, "extracted", state)
	var unfinished int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM source_documents WHERE url = ANY($1) AND state IN ('fetched', 'failed')`,
		[]string{lost, never, gone}).Scan(&unfinished))
	assert.Zero(t, unfinished, "a refetched page is not left for the worker to extract")
}
