package storage

import (
	"context"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A news page dated from a search label in weeks or months ("il y a 1 mois":
// the moment of the search minus one month) goes undated with its fact
// sources; a page dated by its own metadata, or by a label in days, keeps its
// date. Without apply nothing changes.
func TestClearRelativeNewsPageDates(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, `DELETE FROM source_documents WHERE url LIKE 'http://relative.test/%'`)
	require.NoError(t, err)

	fetched := time.Date(2026, 10, 17, 2, 15, 44, 0, time.UTC)
	searched := fetched.Add(-8 * time.Second)
	pages := map[string]time.Time{
		"http://relative.test/month":    searched.AddDate(0, -1, 0),                     // il y a 1 mois
		"http://relative.test/weeks":    searched.AddDate(0, 0, -14),                    // il y a 2 semaines
		"http://relative.test/year":     searched.AddDate(-1, 0, 0),                     // il y a 1 an
		"http://relative.test/days":     searched.AddDate(0, 0, -3),                     // il y a 3 jours: a day
		"http://relative.test/metadata": time.Date(2026, 9, 12, 7, 30, 0, 0, time.UTC),  // the page's own date
		"http://relative.test/midnight": time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC),   // "17 sept. 2026"
		"http://relative.test/sameday":  time.Date(2026, 9, 17, 18, 40, 0, 0, time.UTC), // one month back, another hour
	}
	for url, date := range pages {
		_, err := storer.pool.Exec(ctx, `
			INSERT INTO source_documents (source_type, url, title, content, state, published_at, fetched_at)
			VALUES ('news', $1, 'a page', 'its text', 'extracted', $2, $3)`, url, date, fetched)
		require.NoError(t, err)
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ('a fact of '||$1, $2) RETURNING id`,
			url, pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		_, err = storer.pool.Exec(ctx,
			`INSERT INTO fact_sources (fact_id, source_url, source_type, published_at) VALUES ($1, $2, 'news', $3)`, id, url, date)
		require.NoError(t, err)
	}
	// Two more facts of the page of one month back: one dated on the day the
	// page was read (cleared), one dated by the article's text (kept).
	for _, date := range []time.Time{time.Date(2026, 10, 17, 0, 0, 0, 0, time.UTC), time.Date(2026, 8, 29, 0, 0, 0, 0, time.UTC)} {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ('one more fact', $1) RETURNING id`,
			pgvector.NewHalfVector(mockEmbedding())).Scan(&id))
		_, err = storer.pool.Exec(ctx,
			`INSERT INTO fact_sources (fact_id, source_url, source_type, published_at) VALUES ($1, 'http://relative.test/month', 'news', $2)`, id, date)
		require.NoError(t, err)
	}
	undated := func() []string {
		t.Helper()
		rows, err := storer.pool.Query(ctx, `
			SELECT sd.url FROM source_documents sd JOIN fact_sources fs ON fs.source_url = sd.url
			WHERE sd.url LIKE 'http://relative.test/%' AND sd.published_at IS NULL AND fs.published_at IS NULL`)
		require.NoError(t, err)
		defer rows.Close()
		var out []string
		for rows.Next() {
			var u string
			require.NoError(t, rows.Scan(&u))
			out = append(out, u)
		}
		return out
	}

	p, s, err := storer.ClearRelativeNewsPageDates(ctx, 2*time.Minute, false)
	require.NoError(t, err)
	assert.EqualValues(t, 3, p)
	assert.EqualValues(t, 4, s)
	assert.Empty(t, undated(), "a dry run writes nothing")

	_, _, err = storer.ClearRelativeNewsPageDates(ctx, 2*time.Minute, true)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"http://relative.test/month", "http://relative.test/month", "http://relative.test/weeks", "http://relative.test/year"}, undated())
	var kept int
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT count(*) FROM fact_sources
		WHERE source_url = 'http://relative.test/month' AND published_at = '2026-08-29T00:00:00Z'`).Scan(&kept))
	assert.Equal(t, 1, kept, "a date the article's text gave is kept")
}
