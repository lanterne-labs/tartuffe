package storage

import (
	"context"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// TestEarliestSourceQuote pins the quote that GetFactByID and
// GetContradictionByID return: the quote of the earliest source of the fact.
// The later source is inserted first, so the order of insertion does not
// decide. A source without a date sorts last; a fact without a source gives
// "". The complaint judge shows this quote in its prompts.
func TestEarliestSourceQuote(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('earliest quote person', 'human') RETURNING id`,
	).Scan(&personID))

	type source struct {
		url         string
		quote       string
		publishedAt *time.Time
	}
	day := func(d int) *time.Time {
		v := time.Date(2026, time.June, d, 0, 0, 0, 0, time.UTC)
		return &v
	}
	// insertFact stores one fact with its sources, in the given order, each
	// mentioned by the person (the cross-person guard of RecordContradiction).
	insertFact := func(text string, sources ...source) int64 {
		t.Helper()
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			`INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id`,
			text, pgvector.NewVector(mockEmbedding()),
		).Scan(&id))
		for _, src := range sources {
			var sourceID int64
			require.NoError(t, storer.pool.QueryRow(ctx,
				`INSERT INTO fact_sources (fact_id, source_url, original_quote, published_at, source_type)
				 VALUES ($1, $2, $3, $4, 'test') RETURNING id`,
				id, src.url, src.quote, src.publishedAt,
			).Scan(&sourceID))
			_, err := storer.pool.Exec(ctx,
				`INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)`, sourceID, personID)
			require.NoError(t, err)
		}
		return id
	}

	fa := insertFact("A: rejected the primary",
		source{"http://ex/a-later", "later quote of A", day(6)},
		source{"http://ex/a-earliest", "earliest quote of A", day(5)},
		source{"http://ex/a-undated", "undated quote of A", nil},
	)
	fb := insertFact("B: joined the primary",
		source{"http://ex/b-later", "later quote of B", day(12)},
		source{"http://ex/b-earliest", "earliest quote of B", day(11)},
	)
	noSource := insertFact("C: no source")

	got, err := storer.GetFactByID(ctx, fa)
	require.NoError(t, err)
	require.Equal(t, "earliest quote of A", got.OriginalQuote)
	got, err = storer.GetFactByID(ctx, fb)
	require.NoError(t, err)
	require.Equal(t, "earliest quote of B", got.OriginalQuote)
	got, err = storer.GetFactByID(ctx, noSource)
	require.NoError(t, err)
	require.Equal(t, "", got.OriginalQuote)

	cid, _, err := storer.RecordContradictionImmediate(ctx, personID, fa, fb, "rejected then joined", "")
	require.NoError(t, err)
	contr, err := storer.GetContradictionByID(ctx, cid)
	require.NoError(t, err)
	require.Equal(t, "earliest quote of A", contr.FactAQuote)
	require.Equal(t, "earliest quote of B", contr.FactBQuote)
	require.Equal(t, []string{"http://ex/a-earliest", "http://ex/a-later", "http://ex/a-undated"}, contr.FactASourceURLs,
		"the urls follow the same order as the quotes")
}
