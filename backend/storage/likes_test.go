package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFeedHot: the +1 of users raise an item in the hot order, the viewer's
// are marked, the pages do not overlap, and over all persons only the items
// of the window are read.
func TestFeedHot(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	f := newReportFixture(t, storer, 3)
	_, err := storer.pool.Exec(ctx, `UPDATE persons SET fact_count = 4 WHERE id = $1`, f.person)
	require.NoError(t, err)
	other, err := storer.UpsertAppUser(ctx, "noop", "liker", "Liker")
	require.NoError(t, err)

	_, err = storer.SetLike(ctx, f.user, FeedFact, 1<<40, true)
	assert.ErrorIs(t, err, ErrLikeTarget)
	for _, u := range []int64{f.user, other.ID, f.user} {
		_, err = storer.SetLike(ctx, u, FeedFact, f.facts[0], true)
		require.NoError(t, err)
	}
	n, err := storer.SetLike(ctx, other.ID, FeedContradiction, f.contradiction, true)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	key := func(it FeedItem) string { return fmt.Sprintf("%s:%d", it.Kind, it.ID) }
	hot := FeedFilter{PersonID: f.person, Hot: true, ViewerID: f.user}
	all, err := storer.GetFeed(ctx, hot, nil, 10, 4)
	require.NoError(t, err)
	require.Len(t, all, 4)
	// The oldest fact has 2 likes, the contradiction 1: they come first.
	assert.Equal(t, fmt.Sprintf("fact:%d", f.facts[0]), key(all[0]))
	assert.Equal(t, 2, all[0].Likes)
	assert.True(t, all[0].Liked)
	assert.Equal(t, fmt.Sprintf("contradiction:%d", f.contradiction), key(all[1]))
	assert.False(t, all[1].Liked, "liked by the other user only")

	var paged []string
	for off := 0; off < 4; off += 2 {
		hot.Offset = off
		page, err := storer.GetFeed(ctx, hot, nil, 2, 4)
		require.NoError(t, err)
		for _, it := range page {
			paged = append(paged, key(it))
		}
	}
	var want []string
	for _, it := range all {
		want = append(want, key(it))
	}
	assert.Equal(t, want, paged)

	// Over all persons, the window: the fixture's items are weeks old.
	window, err := storer.GetFeed(ctx, FeedFilter{Hot: true}, nil, 10, 4)
	require.NoError(t, err)
	assert.Empty(t, window)
	_, err = storer.pool.Exec(ctx, `UPDATE facts SET created_at = now() WHERE id = $1`, f.facts[2])
	require.NoError(t, err)
	window, err = storer.GetFeed(ctx, FeedFilter{Hot: true}, nil, 10, 4)
	require.NoError(t, err)
	require.Len(t, window, 1)
	assert.Equal(t, fmt.Sprintf("fact:%d", f.facts[2]), key(window[0]))

	n, err = storer.SetLike(ctx, f.user, FeedFact, f.facts[0], false)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}
