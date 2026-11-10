package storage

import (
	"context"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"digital-mirror/backend"
)

// A 2013 statement recalled by a 2026 article is a 2013 fact: the feed and
// the explanation writer give its statement date with its precision, and
// put it before the 2025 statement it contradicts, whatever the dates of the
// articles. The date finder is given the article of a fact, and again the
// facts an earlier run checked without it.
func TestStatementDateShownAndRechecked(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, attributes) VALUES ('dated person', 'human', '{"candidate": true}') RETURNING id`,
	).Scan(&personID))
	insertFact := func(text, url string, published time.Time) int64 {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id`,
			text, pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		_, err := storer.pool.Exec(ctx, `
			WITH s AS (INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type, published_at)
			           VALUES ($1, $2, 'a quote', 'news', $3) RETURNING id)
			INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $4 FROM s`, id, url, published, personID)
		require.NoError(t, err)
		return id
	}
	recalled := insertFact("in 2013, asked for ineligibility for life", "http://dated.test/recalled", time.Date(2026, 7, 10, 8, 0, 0, 0, time.UTC))
	recent := insertFact("says nothing will stop the candidacy", "http://dated.test/recent", time.Date(2025, 8, 14, 8, 0, 0, 0, time.UTC))
	cid, _, err := storer.RecordContradictionImmediate(ctx, personID, recalled, recent, "reason", "")
	require.NoError(t, err)

	// The date finder: both facts wait; the one whose article is stored comes with it.
	_, err = storer.pool.Exec(ctx, `
		INSERT INTO source_documents (source_type, url, person_id, title, content)
		VALUES ('news', 'http://dated.test/recalled', $1, 'a portrait', 'En 2013, elle réclamait…')`, personID)
	require.NoError(t, err)
	listed := func(recheck *time.Time) map[int64]StatementDateFact {
		t.Helper()
		facts, err := storer.ListFactsForStatementDate(ctx, true, 100000, recheck)
		require.NoError(t, err)
		out := map[int64]StatementDateFact{}
		for _, f := range facts {
			if f.ID == recalled || f.ID == recent {
				out[f.ID] = f
			}
		}
		return out
	}
	got := listed(nil)
	require.Len(t, got, 2)
	assert.Equal(t, "En 2013, elle réclamait…", got[recalled].Article)
	assert.Equal(t, "a portrait", got[recalled].ArticleTitle)
	assert.Empty(t, got[recent].Article)

	// An earlier run read the facts alone and found no date.
	require.NoError(t, storer.SetStatementDate(ctx, recalled, nil))
	require.NoError(t, storer.SetStatementDate(ctx, recent, nil))
	_, err = storer.pool.Exec(ctx, `UPDATE facts SET created_at = now() - interval '1 day' WHERE id = ANY($1)`, []int64{recalled, recent})
	require.NoError(t, err)
	assert.Empty(t, listed(nil), "a checked fact is not asked again")
	runStart := time.Now().Add(time.Minute)
	got = listed(&runStart)
	require.Len(t, got, 1, "only the fact whose article is stored is read again")
	assert.Contains(t, got, recalled)

	// This run finds 2013; the fact is done for this run.
	y2013, err := backend.ParseStatementDate("2013")
	require.NoError(t, err)
	require.NoError(t, storer.SetStatementDate(ctx, recalled, &y2013))
	assert.Empty(t, listed(&runStart))
	// A fact this run checked without a date is not listed again either.
	require.NoError(t, storer.SetStatementDate(ctx, recalled, nil))
	before := time.Now().Add(-time.Minute)
	assert.Empty(t, listed(&before))
	require.NoError(t, storer.SetStatementDate(ctx, recalled, &y2013))

	// The explanation writer reads 2013, with the article's date beside it.
	todo, err := storer.ListContradictionsToExplain(ctx, "1", ExplainFilter{IDs: []int64{cid}})
	require.NoError(t, err)
	require.Len(t, todo, 1)
	side := todo[0].A
	if side.FactID != recalled {
		side = todo[0].B
	}
	require.NotNil(t, side.Date)
	assert.Equal(t, "2013", backend.FormatFactDate(side.Date))
	require.NotNil(t, side.SourceDate)
	assert.Equal(t, 2026, side.SourceDate.Year())

	// The feed: the 2013 statement first, as a year.
	_, err = storer.pool.Exec(ctx, `UPDATE persons SET fact_count = 100 WHERE id = $1`, personID)
	require.NoError(t, err)
	page, err := storer.GetFeed(ctx, FeedFilter{PersonID: personID, Kind: FeedContradiction}, nil, 10, 0)
	require.NoError(t, err)
	var item *FeedItem
	for i := range page {
		if page[i].ID == cid {
			item = &page[i]
		}
	}
	require.NotNil(t, item, "the contradiction is in the feed")
	assert.Equal(t, recalled, item.Fact.ID, "the earlier statement comes first")
	require.NotNil(t, item.Fact.Date)
	assert.Equal(t, 2013, item.Fact.Date.UTC().Year())
	require.NotNil(t, item.Fact.DatePrecision)
	assert.Equal(t, "year", *item.Fact.DatePrecision)
	assert.Nil(t, item.Other.DatePrecision, "a source's date has no precision")
	assert.Equal(t, 2025, item.Other.Date.UTC().Year())
}
