package storage

import (
	"context"
	"fmt"
	"errors"
	"sync"
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
	_, err := storer.pool.Exec(ctx, "TRUNCATE app_users, poll_events, auth_challenges RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	var firstEvent int64
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM event_log`).Scan(&firstEvent))

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
	u2, err := storer.UpsertAppUser(ctx, "google", "two", "")
	require.NoError(t, err)
	// Without a verified identity, an account votes for nobody.
	_, err = storer.UpdatePollBallot(ctx, eventID, u1.ID, true, func([]int64) ([]int64, error) { return []int64{a}, nil })
	require.ErrorIs(t, err, ErrNotVerified)
	for i, u := range []*AppUser{u1, u2} {
		_, err = storer.AttachVerification(ctx, u.ID, fmt.Sprintf("noop:doc%d", i))
		require.NoError(t, err)
	}

	set := func(ids ...int64) func([]int64) ([]int64, error) {
		return func([]int64) ([]int64, error) { return ids, nil }
	}
	_, err = storer.UpdatePollBallot(ctx, eventID, u1.ID, true, set(b))
	require.NoError(t, err)
	got, err := storer.UpdatePollBallot(ctx, eventID, u1.ID, true, func(current []int64) ([]int64, error) {
		assert.Equal(t, []int64{b}, current, "edit gets the stored ballot")
		return []int64{a, b}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, []int64{a, b}, got)
	_, err = storer.UpdatePollBallot(ctx, eventID, u1.ID, true, set(a, b))
	require.NoError(t, err, "an unchanged ballot logs no event")
	refused := errors.New("refused")
	_, err = storer.UpdatePollBallot(ctx, eventID, u1.ID, true, func([]int64) ([]int64, error) { return nil, refused })
	assert.ErrorIs(t, err, refused)
	other, err := storer.FindOrInsertPersonWithType(ctx, "not a candidate", "human")
	require.NoError(t, err)
	_, err = storer.UpdatePollBallot(ctx, eventID, u1.ID, true, set(other))
	assert.ErrorIs(t, err, ErrNotCandidate)
	_, err = storer.UpdatePollBallot(ctx, eventID, u2.ID, true, set(a))
	require.NoError(t, err)
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
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT COUNT(*) FROM event_log WHERE topic = 'poll_updated' AND entity_id = $1 AND id > $2`, eventID, firstEvent).Scan(&updates))
	assert.Equal(t, 3, updates, "one event per saved ballot")

	// A challenge serves once, for its provider, before it expires.
	require.NoError(t, storer.CreateAuthChallenge(ctx, "zkpassport", "c1", time.Now().Add(time.Minute)))
	require.NoError(t, storer.CreateAuthChallenge(ctx, "zkpassport", "old", time.Now().Add(-time.Minute)))
	ok, err := storer.ConsumeAuthChallenge(ctx, "noop", "c1")
	require.NoError(t, err)
	assert.False(t, ok, "another provider's challenge")
	ok, err = storer.ConsumeAuthChallenge(ctx, "zkpassport", "c1")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = storer.ConsumeAuthChallenge(ctx, "zkpassport", "c1")
	require.NoError(t, err)
	assert.False(t, ok, "used once already")
	ok, err = storer.ConsumeAuthChallenge(ctx, "zkpassport", "old")
	require.NoError(t, err)
	assert.False(t, ok, "expired")
}

// TestPollBallotConcurrentVotes: the votes one user casts at once (two tabs)
// apply one after the other, so the ballot never holds more than the votes
// allowed, and erasing the user drops their ballot and refreshes the pages.
func TestPollBallotConcurrentVotes(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, "TRUNCATE app_users, poll_events, auth_challenges RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	eventID, err := storer.SavePollEvent(ctx, PollEvent{Slug: "race-poll", TitleFR: "Race", TitleEN: "Race",
		MaxChoices: 3, MinVoters: 1, OpensAt: time.Now().Add(-time.Hour)})
	require.NoError(t, err)
	var candidates []int64
	for _, name := range []string{"race a", "race b", "race c", "race d", "race e", "race f"} {
		id, err := storer.FindOrInsertPersonWithType(ctx, name, "human")
		require.NoError(t, err)
		_, err = storer.AddPollCandidate(ctx, eventID, id)
		require.NoError(t, err)
		candidates = append(candidates, id)
	}
	u, err := storer.UpsertAppUser(ctx, "noop", "racer", "Racer")
	require.NoError(t, err)
	_, err = storer.AttachVerification(ctx, u.ID, "noop:racer")
	require.NoError(t, err)

	full := errors.New("full")
	var wg sync.WaitGroup
	errs := make(chan error, len(candidates))
	for _, id := range candidates {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_, err := storer.UpdatePollBallot(ctx, eventID, u.ID, true, func(current []int64) ([]int64, error) {
				if len(current) >= 3 {
					return nil, full
				}
				return append(current, id), nil
			})
			errs <- err
		}(id)
	}
	wg.Wait()
	close(errs)
	refusedFull := 0
	for err := range errs {
		if errors.Is(err, full) {
			refusedFull++
		} else {
			require.NoError(t, err)
		}
	}
	assert.Equal(t, 3, refusedFull, "three votes fit, three are refused")
	ballot, err := storer.GetPollBallot(ctx, eventID, u.ID)
	require.NoError(t, err)
	assert.Len(t, ballot, 3)

	choices, err := storer.GetUserPollChoices(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, choices, 3)
	for i, c := range choices {
		assert.Equal(t, ballot[i], c.PersonID)
		assert.Equal(t, i+1, c.Rank)
		assert.NotEmpty(t, c.Name)
	}

	var before int64
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT COALESCE(MAX(id), 0) FROM event_log`).Scan(&before))
	require.NoError(t, storer.DeleteAppUser(ctx, u.ID))
	choices, err = storer.GetUserPollChoices(ctx, u.ID)
	require.NoError(t, err)
	assert.Empty(t, choices)
	var updates int
	require.NoError(t, storer.pool.QueryRow(ctx, `SELECT COUNT(*) FROM event_log WHERE topic = 'poll_updated' AND entity_id = $1 AND id > $2`, eventID, before).Scan(&updates))
	assert.Equal(t, 1, updates)
	_, err = storer.UpdatePollBallot(ctx, eventID, u.ID, true, func(c []int64) ([]int64, error) { return c, nil })
	assert.Error(t, err, "a deleted user has no ballot to change")
}
