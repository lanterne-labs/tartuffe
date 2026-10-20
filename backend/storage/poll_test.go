package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPollBallotsAndTallies: a ballot replaces the user's previous one, the
// first choice is the lowest rank left once a candidate is removed, and
// purging a provider drops its users' ballots.
func TestPollBallotsAndTallies(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE app_users, poll_events RESTART IDENTITY CASCADE")
	require.NoError(t, err)

	closes := time.Now().Add(time.Hour)
	eventID, err := storer.SavePollEvent(ctx, PollEvent{Slug: "test-poll", TitleFR: "Test", TitleEN: "Test",
		MaxChoices: 3, MinVoters: 1, OpensAt: time.Now().Add(-time.Hour), ClosesAt: &closes, Featured: true})
	require.NoError(t, err)
	require.NoError(t, storer.AddPollBlackout(ctx, eventID, closes.Add(-time.Minute), closes))

	a, err := storer.FindOrInsertPersonWithType(ctx, "poll candidate a", "human")
	require.NoError(t, err)
	b, err := storer.FindOrInsertPersonWithType(ctx, "poll candidate b", "human")
	require.NoError(t, err)
	for _, id := range []int64{a, b} {
		added, err := storer.AddPollCandidate(ctx, eventID, id)
		require.NoError(t, err)
		assert.True(t, added)
	}
	added, err := storer.AddPollCandidate(ctx, eventID, a)
	require.NoError(t, err)
	assert.False(t, added, "already a candidate")

	e, err := storer.GetPollEvent(ctx, "test-poll")
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.Len(t, e.Blackouts, 1)
	events, err := storer.ListPollEvents(ctx)
	require.NoError(t, err)
	assert.Equal(t, "test-poll", events[0].Slug, "featured first")

	u1, err := storer.UpsertAppUser(ctx, "noop", "one", "One")
	require.NoError(t, err)
	again, err := storer.UpsertAppUser(ctx, "noop", "one", "One")
	require.NoError(t, err)
	assert.Equal(t, u1.ID, again.ID)
	assert.Nil(t, u1.ConsentAt)
	u2, err := storer.UpsertAppUser(ctx, "zkpassport", "two", "")
	require.NoError(t, err)

	require.NoError(t, storer.SavePollBallot(ctx, eventID, u1.ID, []int64{b}))
	require.NoError(t, storer.SavePollBallot(ctx, eventID, u1.ID, []int64{a, b}), "replaces the first ballot")
	require.NoError(t, storer.SavePollBallot(ctx, eventID, u2.ID, []int64{a}))
	ballot, err := storer.GetPollBallot(ctx, eventID, u1.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{a, b}, ballot)

	tallies, voters, err := storer.GetPollTallies(ctx, eventID)
	require.NoError(t, err)
	assert.Equal(t, 2, voters)
	byID := map[int64]PollCandidateTally{}
	for _, c := range tallies {
		byID[c.PersonID] = c
	}
	assert.Equal(t, 2, byID[a].FirstChoices)
	assert.Equal(t, 2, byID[a].Chosen)
	assert.Equal(t, 0, byID[b].FirstChoices)
	assert.Equal(t, 1, byID[b].Chosen)

	// Removing a: u1's ballot now counts b first; u2's ballot is empty.
	removed, err := storer.RemovePollCandidate(ctx, eventID, a)
	require.NoError(t, err)
	assert.True(t, removed)
	tallies, voters, err = storer.GetPollTallies(ctx, eventID)
	require.NoError(t, err)
	assert.Equal(t, 1, voters)
	require.Len(t, tallies, 1)
	assert.Equal(t, 1, tallies[0].FirstChoices)

	// Sessions: a live token finds its user, an ended one does not.
	hash := []byte("0123456789abcdef0123456789abcdef")
	require.NoError(t, storer.CreateUserSession(ctx, u1.ID, hash, time.Now().Add(time.Hour)))
	su, err := storer.GetSessionUser(ctx, hash)
	require.NoError(t, err)
	require.NotNil(t, su)
	assert.Equal(t, u1.ID, su.ID)
	assert.NotNil(t, su.ConsentAt, "a saved ballot records the consent")
	require.NoError(t, storer.DeleteUserSession(ctx, hash))
	su, err = storer.GetSessionUser(ctx, hash)
	require.NoError(t, err)
	assert.Nil(t, su)

	n, err := storer.PurgeAppUsers(ctx, "noop")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	_, voters, err = storer.GetPollTallies(ctx, eventID)
	require.NoError(t, err)
	assert.Equal(t, 0, voters)

	var updates int
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT COUNT(*) FROM event_log WHERE topic = 'poll_updated' AND entity_id = $1`, eventID).Scan(&updates))
	assert.Equal(t, 3, updates, "one event per saved ballot")
}
