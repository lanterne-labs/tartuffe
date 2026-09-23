package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"

	pgvector "github.com/pgvector/pgvector-go"
	"github.com/stretchr/testify/require"
)

// Members come back most concrete first (digits, then length), deduplicated,
// within the character budget — the first one always; a fact without members
// is absent.
func TestGroupMemberTexts(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, _, err := storer.AddPerson(ctx, "group members person")
	require.NoError(t, err)
	fact := func(text string) int64 {
		var id int64
		require.NoError(t, storer.pool.QueryRow(ctx,
			"INSERT INTO facts (fact_text, embedding) VALUES ($1, $2) RETURNING id", text, pgvector.NewVector(mockEmbedding())).Scan(&id))
		return id
	}
	parent, err := storer.CreateMetaFact(ctx, pid, "un plan pour ramener le déficit", "slug", mockEmbedding(), "fr", "")
	require.NoError(t, err)
	vague := fact("Il veut réduire le déficit.")
	precise := fact("Un effort de 43,8 milliards d'euros pour ramener le déficit sous 5 % du PIB.")
	fiveYears := fact("Un plan de 5 ans pour ramener les déficits sous la croissance.")
	for _, m := range []int64{vague, precise, fiveYears} {
		require.NoError(t, storer.MarkFactAsDuplicate(ctx, m, parent))
	}
	lone := fact("un fait sans groupe")

	got, err := storer.GroupMemberTexts(ctx, []int64{parent, lone})
	require.NoError(t, err)
	require.Equal(t, []string{
		"Un effort de 43,8 milliards d'euros pour ramener le déficit sous 5 % du PIB.",
		"Un plan de 5 ans pour ramener les déficits sous la croissance.",
		"Il veut réduire le déficit.",
	}, got[parent], "most digits first, then longest")
	_, hasLone := got[lone]
	require.False(t, hasLone, "a fact without members is absent")

	big, err := storer.CreateMetaFact(ctx, pid, "gros groupe", "slug", mockEmbedding(), "fr", "")
	require.NoError(t, err)
	for i := 0; i < 5; i++ {
		m := fact(fmt.Sprintf("Membre %d : %s", i, strings.Repeat("x", 300)))
		require.NoError(t, storer.MarkFactAsDuplicate(ctx, m, big))
	}
	got, err = storer.GroupMemberTexts(ctx, []int64{big})
	require.NoError(t, err)
	require.Len(t, got[big], 2, "two ~311-char members fit in 800, a third does not")

	empty, err := storer.GroupMemberTexts(ctx, nil)
	require.NoError(t, err)
	require.Empty(t, empty)
}
