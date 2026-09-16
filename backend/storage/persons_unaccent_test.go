package storage

import (
	"context"
	"testing"

	"digital-mirror/backend/core"
	"digital-mirror/backend/util"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUnaccentMergeAndCanonicalize exercises the `dm persons unaccent` heal flow:
// two rows that canonicalize identically under util.NormalizePersonName (here one
// carries an accent + a curly apostrophe, the other is plain ASCII) are detected as
// one group, merged into the higher-fact survivor, and the survivor name is
// rewritten to the canonical form.
func TestUnaccentMergeAndCanonicalize(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	const fancy = "Éric D’Testperson" // accent + curly apostrophe
	const plain = "eric d'testperson" // ASCII; same canonical form
	require.Equal(t, util.NormalizePersonName(fancy), util.NormalizePersonName(plain),
		"the two surface forms must canonicalize identically")

	var richID, plainID, ctrlID int64
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, fact_count) VALUES ($1, 'human', 5) RETURNING id`, fancy).Scan(&richID))
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, fact_count) VALUES ($1, 'human', 2) RETURNING id`, plain).Scan(&plainID))
	require.NoError(t, storer.pool.QueryRow(ctx,
		`INSERT INTO persons (name, entity_type, fact_count) VALUES ('solo testperson', 'human', 1) RETURNING id`).Scan(&ctrlID))

	// 1. Group all persons by the canonical form (mirrors the command).
	members, err := storer.GetAllDedupMembers(ctx)
	require.NoError(t, err)
	groups := map[string][]core.PersonDedupMember{}
	for _, m := range members {
		groups[util.NormalizePersonName(m.Name)] = append(groups[util.NormalizePersonName(m.Name)], m)
	}
	ourGroup := groups[util.NormalizePersonName(plain)]
	require.Len(t, ourGroup, 2, "the accent+apostrophe variant and the ASCII row form one group")

	// 2. Survivor is the higher-fact row; merge the other in.
	survivor, ok := ChooseCanonical(ourGroup)
	require.True(t, ok)
	assert.Equal(t, richID, survivor.ID)
	require.NoError(t, storer.MergePersons(ctx, plainID, survivor.ID))

	// 3. Canonicalize remaining names.
	members, err = storer.GetAllDedupMembers(ctx)
	require.NoError(t, err)
	for _, m := range members {
		if c := util.NormalizePersonName(m.Name); c != m.Name {
			require.NoError(t, storer.RenamePerson(ctx, m.ID, c))
		}
	}

	// 4. One row remains, name is the canonical ASCII form; merged-away row gone; control intact.
	var name string
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT name FROM persons WHERE id=$1`, richID).Scan(&name))
	assert.Equal(t, "eric d'testperson", name)

	var gone int
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT count(*) FROM persons WHERE id=$1`, plainID).Scan(&gone))
	assert.Equal(t, 0, gone)

	var ctrlName string
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT name FROM persons WHERE id=$1`, ctrlID).Scan(&ctrlName))
	assert.Equal(t, "solo testperson", ctrlName)
}
