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

// A fact dated to a year is listed for the search of its day; the day found
// becomes its statement date and it is not listed again, nor is a fact
// searched without a day (unless asked again).
func TestStatementDaySearch(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, attributes) VALUES ('day person', 'human', '{"candidate": true}') RETURNING id`,
	).Scan(&personID))
	insertFact := func(text, date, precision string) int64 {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding, statement_date, statement_date_precision) VALUES ($1, $2, $3::date, $4) RETURNING id`,
			text, pgvector.NewHalfVector(mockEmbedding()), date, precision).Scan(&id))
		_, err := storer.pool.Exec(ctx, `
			WITH s AS (INSERT INTO fact_sources (fact_id, source_url, original_quote, source_type, published_at)
			           VALUES ($1::bigint, 'https://example.org/day/' || $1::bigint::text, 'a quote', 'news', '2026-05-01') RETURNING id)
			INSERT INTO fact_mentions (fact_source_id, person_id) SELECT id, $2 FROM s`, id, personID)
		require.NoError(t, err)
		return id
	}
	year := insertFact("said it in 2013", "2013-01-01", "year")
	month := insertFact("said it in March 2022", "2022-03-01", "month")
	insertFact("said it on a day", "2024-03-14", "day")

	ids := func(again bool) []int64 {
		t.Helper()
		list, err := storer.ListFactsForStatementDay(ctx, true, 10, again)
		require.NoError(t, err)
		var out []int64
		for _, p := range list {
			if p.Person == "day person" {
				out = append(out, p.FactID)
				assert.NotEmpty(t, backend.DatePrecisionOf(p.Known))
				assert.Equal(t, "a quote", p.Quote)
				require.NotNil(t, p.SourceDate)
			}
		}
		return out
	}
	assert.ElementsMatch(t, []int64{year, month}, ids(false))

	day := backend.WithDatePrecision(time.Date(2013, 4, 8, 0, 0, 0, 0, time.UTC), backend.DatePrecisionDay)
	require.NoError(t, storer.SetStatementDay(ctx, year, &day))
	require.NoError(t, storer.SetStatementDay(ctx, month, nil))
	assert.Empty(t, ids(false))
	assert.Equal(t, []int64{month}, ids(true))

	var date, precision string
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT statement_date::text, statement_date_precision FROM facts WHERE id = $1`, year).Scan(&date, &precision))
	assert.Equal(t, "2013-04-08", date)
	assert.Equal(t, "day", precision)
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT statement_date::text, statement_date_precision FROM facts WHERE id = $1`, month).Scan(&date, &precision))
	assert.Equal(t, "2022-03-01", date)
	assert.Equal(t, "month", precision)
}
