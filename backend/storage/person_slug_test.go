package storage

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPersonSlug covers the slug a person gets at the insert (the trigger
// persons_slug): the name folded to lowercase ASCII and hyphens, a suffix
// when another person already has it, a letter in front of digits, the id
// when the name has no Latin letter; and that the API's readers give it.
func TestPersonSlug(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	id, _, err := storer.AddPerson(ctx, "Jean-Luc Mélenchon Test")
	require.NoError(t, err)
	p, err := storer.GetPersonByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "jean-luc-melenchon-test", p.Slug)

	got, err := storer.GetPersonIDBySlug(ctx, "jean-luc-melenchon-test")
	require.NoError(t, err)
	assert.Equal(t, id, got)
	got, err = storer.GetPersonIDBySlug(ctx, "nobody-test")
	require.NoError(t, err)
	assert.Zero(t, got, "an unknown slug is 0, not an error")

	// The slug never changes: a rename keeps the links alive.
	_, err = storer.pool.Exec(ctx, `UPDATE persons SET name = 'Jean-Luc Melenchon Renamed' WHERE id = $1`, id)
	require.NoError(t, err)
	p, err = storer.GetPersonByID(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "jean-luc-melenchon-test", p.Slug)

	// Two names with one fold (the Go entry points resolve them to one
	// person; here the rows are inserted as is): the second gets a suffix.
	var slugs []string
	rows, err := storer.pool.Query(ctx, `INSERT INTO persons (name) VALUES ('Anne Marie Test'), ('Anne  Marie Test'), ('2027'), ('李') RETURNING slug`)
	require.NoError(t, err)
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		slugs = append(slugs, s)
	}
	require.NoError(t, rows.Err())
	require.Len(t, slugs, 4)
	assert.Equal(t, "anne-marie-test", slugs[0])
	assert.Equal(t, "anne-marie-test-2", slugs[1])
	assert.Equal(t, "p-2027", slugs[2], "digits alone would read as an id")
	assert.Regexp(t, `^p-\d+$`, slugs[3], "no Latin letter: the id names the person")

	// The members of an entity carry their slug too.
	party, _, err := storer.AddPersonWithType(ctx, "Parti Test", "party")
	require.NoError(t, err)
	_, err = storer.pool.Exec(ctx, `INSERT INTO person_links (src_person_id, dst_person_id, link_type) VALUES ($1, $2, 'affiliation')`, id, party)
	require.NoError(t, err)
	members, err := storer.GetPersonMembers(ctx, party)
	require.NoError(t, err)
	require.Len(t, members, 1)
	assert.Equal(t, "jean-luc-melenchon-test", members[0].Slug)

	cards, err := storer.GetPersonCards(ctx, []int64{id})
	require.NoError(t, err)
	assert.Equal(t, "jean-luc-melenchon-test", cards[id].Slug)
}
