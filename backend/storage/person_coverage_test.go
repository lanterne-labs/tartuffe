package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetPersonCoverage: a fact is counted in the year of its statement date,
// else of its earliest source; the years between the first and the last (and
// up to this year) are listed at 0; a duplicate and a dateless fact are not on
// a year, the dateless one counted apart.
func TestGetPersonCoverage(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var personID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('coverage person', 'human') RETURNING id`,
	).Scan(&personID))

	thisYear := time.Now().UTC().Year()
	at := func(y int) *time.Time {
		d := time.Date(y, 6, 1, 12, 0, 0, 0, time.UTC)
		return &d
	}
	old := insertDatedFact(t, storer, personID, "C: said one thing long ago", at(thisYear-4))
	insertDatedFact(t, storer, personID, "C: said another thing", at(thisYear-1))
	recent := insertDatedFact(t, storer, personID, "C: said a third thing", at(thisYear-1))
	// The statement date wins over the source's publication.
	_, err := storer.pool.Exec(ctx, `UPDATE facts SET statement_date = $2 WHERE id = $1`, recent, time.Date(thisYear-2, 3, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	// A duplicate of the old fact is not counted twice.
	dup := insertDatedFact(t, storer, personID, "C: said one thing long ago (again)", at(thisYear-4))
	_, err = storer.pool.Exec(ctx,
		`INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type) VALUES ($1, $2, 'duplicate')`, old, dup)
	require.NoError(t, err)
	// A fact without a date is counted apart.
	insertDatedFact(t, storer, personID, "C: said something undated", nil)

	cov, err := storer.GetPersonCoverage(ctx, personID)
	require.NoError(t, err)
	assert.Equal(t, 4, cov.Total)
	assert.Equal(t, 1, cov.Undated)
	assert.Equal(t, []CoverageYear{
		{Year: thisYear - 4, Facts: 1},
		{Year: thisYear - 3, Facts: 0},
		{Year: thisYear - 2, Facts: 1},
		{Year: thisYear - 1, Facts: 1},
		{Year: thisYear, Facts: 0},
	}, cov.Years)

	// Nobody: an empty histogram, not an error.
	var emptyID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type) VALUES ('nobody', 'human') RETURNING id`,
	).Scan(&emptyID))
	empty, err := storer.GetPersonCoverage(ctx, emptyID)
	require.NoError(t, err)
	assert.Equal(t, &Coverage{Years: []CoverageYear{}}, empty)
}
