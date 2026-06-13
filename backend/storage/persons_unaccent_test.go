package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnaccentMergeAndStrip exercises the `dm persons unaccent` heal flow end to
// end: detect an accent-duplicate group, pick the higher-fact survivor, merge the
// other in, then strip all names to their accent-free form.
func TestUnaccentMergeAndStrip(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	var accentedID, plainID, ctrlID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, fact_count) VALUES ('éric testperson', 'human', 5) RETURNING id`).Scan(&accentedID))
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, fact_count) VALUES ('eric testperson', 'human', 2) RETURNING id`).Scan(&plainID))
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, fact_count) VALUES ('solo testperson', 'human', 1) RETURNING id`).Scan(&ctrlID))

	// 1. The éric/eric pair is detected as one group; the control is not.
	groups, err := storer.GetAccentDuplicateGroups(ctx)
	require.NoError(t, err)
	var ourGroup []DedupMember
	for _, g := range groups {
		for _, m := range g {
			if m.ID == accentedID || m.ID == plainID {
				ourGroup = g
			}
		}
	}
	require.Len(t, ourGroup, 2, "the éric/eric pair forms one accent-dup group")

	// 2. Survivor is the higher-fact-count row (the accented one, 5 facts).
	survivor, ok := ChooseCanonical(ourGroup)
	require.True(t, ok)
	assert.Equal(t, accentedID, survivor.ID)

	// 3. Merge the other in, then 4. strip all accents.
	require.NoError(t, storer.MergePersons(ctx, plainID, survivor.ID))
	stripped, err := storer.StripAllPersonAccents(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, stripped, int64(1), "at least the survivor name was stripped")

	// 5. One row remains, name is accent-free; the merged-away row is gone.
	var remaining int
	require.NoError(t, storer.pool.QueryRow(ctx,
		`SELECT count(*) FROM persons WHERE lower(immutable_unaccent(name))='eric testperson'`).Scan(&remaining))
	assert.Equal(t, 1, remaining, "accent variants collapsed to one row")

	var name string
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT name FROM persons WHERE id=$1`, accentedID).Scan(&name))
	assert.Equal(t, "eric testperson", name, "survivor name is accent-free")

	var gone int
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT count(*) FROM persons WHERE id=$1`, plainID).Scan(&gone))
	assert.Equal(t, 0, gone, "merged-away row deleted")

	// Control row untouched.
	var ctrlName string
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT name FROM persons WHERE id=$1`, ctrlID).Scan(&ctrlName))
	assert.Equal(t, "solo testperson", ctrlName)
}
