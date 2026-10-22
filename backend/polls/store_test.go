package polls_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/polls"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stores are the polls and the accounts on the test database, wired as in
// the server, with a clean slate of accounts and polls.
type stores struct {
	storer *storage.PostgresStorer
	polls  *polls.PGStore
	acc    *accounts.PGStore
}

func openStores(t *testing.T) stores {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set, skipping database tests")
	}
	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	t.Cleanup(storer.Close)
	_, err = storer.Pool().Exec(ctx, "TRUNCATE app_users, poll_events, auth_challenges RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	ps := polls.NewPGStore(storer.Pool(), accounts.LockVerified)
	acc := accounts.NewPGStore(storer.Pool(), accounts.Hooks{
		Moved:    []accounts.MovedHook{ps.MoveBallots},
		Deleting: []accounts.DeletingHook{ps.ForgetUser},
	})
	return stores{storer: storer, polls: ps, acc: acc}
}

func (s stores) lastEvent(t *testing.T) int64 {
	var id int64
	require.NoError(t, s.storer.Pool().QueryRow(context.Background(), `SELECT COALESCE(MAX(id), 0) FROM event_log`).Scan(&id))
	return id
}

func (s stores) updatesSince(t *testing.T, eventID, since int64) int {
	var n int
	require.NoError(t, s.storer.Pool().QueryRow(context.Background(),
		`SELECT COUNT(*) FROM event_log WHERE topic = 'poll_updated' AND entity_id = $1 AND id > $2`, eventID, since).Scan(&n))
	return n
}

func set(ids ...int64) func([]int64) ([]int64, error) {
	return func([]int64) ([]int64, error) { return ids, nil }
}

// TestBallotsAndTallies: a ballot replaces the user's previous one, only a
// verified account adds votes, the first choice is the lowest rank left once
// a candidate is removed, and purging a provider drops its users' ballots.
func TestBallotsAndTallies(t *testing.T) {
	s := openStores(t)
	ctx := context.Background()
	firstEvent := s.lastEvent(t)

	closes := time.Now().Add(time.Hour)
	eventID, err := s.polls.SaveEvent(ctx, polls.Event{Slug: "test-poll", TitleFR: "Test", TitleEN: "Test",
		MaxChoices: 3, MinVoters: 1, OpensAt: time.Now().Add(-time.Hour), ClosesAt: &closes, Featured: true})
	require.NoError(t, err)
	require.NoError(t, s.polls.AddBlackout(ctx, eventID, closes.Add(-time.Minute), closes))

	a, err := s.storer.FindOrInsertPersonWithType(ctx, "poll candidate a", "human")
	require.NoError(t, err)
	b, err := s.storer.FindOrInsertPersonWithType(ctx, "poll candidate b", "human")
	require.NoError(t, err)
	for _, id := range []int64{a, b} {
		added, err := s.polls.AddCandidate(ctx, eventID, id)
		require.NoError(t, err)
		assert.True(t, added)
	}
	added, err := s.polls.AddCandidate(ctx, eventID, a)
	require.NoError(t, err)
	assert.False(t, added, "already a candidate")

	e, err := s.polls.GetEvent(ctx, "test-poll")
	require.NoError(t, err)
	require.NotNil(t, e)
	assert.Len(t, e.Blackouts, 1)
	events, err := s.polls.ListEvents(ctx)
	require.NoError(t, err)
	assert.Equal(t, "test-poll", events[0].Slug, "featured first")

	u1, err := s.acc.UpsertUser(ctx, "noop", "one", "One")
	require.NoError(t, err)
	u2, err := s.acc.UpsertUser(ctx, "google", "two", "")
	require.NoError(t, err)
	_, err = s.polls.UpdateBallot(ctx, eventID, u1.ID, true, set(a))
	require.ErrorIs(t, err, polls.ErrNotVerified, "without a verified identity, an account votes for nobody")
	for i, u := range []*accounts.User{u1, u2} {
		_, err = s.acc.AttachVerification(ctx, u.ID, []string{"noop:doc1", "noop:doc2"}[i])
		require.NoError(t, err)
	}

	consented, err := s.polls.Consented(ctx, u1.ID)
	require.NoError(t, err)
	assert.False(t, consented)
	_, err = s.polls.UpdateBallot(ctx, eventID, u1.ID, true, set(b))
	require.NoError(t, err)
	consented, err = s.polls.Consented(ctx, u1.ID)
	require.NoError(t, err)
	assert.True(t, consented, "a saved ballot records the consent")
	got, err := s.polls.UpdateBallot(ctx, eventID, u1.ID, true, func(current []int64) ([]int64, error) {
		assert.Equal(t, []int64{b}, current, "edit gets the stored ballot")
		return []int64{a, b}, nil
	})
	require.NoError(t, err)
	assert.Equal(t, []int64{a, b}, got)
	_, err = s.polls.UpdateBallot(ctx, eventID, u1.ID, true, set(a, b))
	require.NoError(t, err, "an unchanged ballot logs no event")
	refused := errors.New("refused")
	_, err = s.polls.UpdateBallot(ctx, eventID, u1.ID, true, func([]int64) ([]int64, error) { return nil, refused })
	assert.ErrorIs(t, err, refused)
	other, err := s.storer.FindOrInsertPersonWithType(ctx, "not a candidate", "human")
	require.NoError(t, err)
	_, err = s.polls.UpdateBallot(ctx, eventID, u1.ID, true, set(other))
	assert.ErrorIs(t, err, polls.ErrNotCandidate)
	_, err = s.polls.UpdateBallot(ctx, eventID, u2.ID, true, set(a))
	require.NoError(t, err)
	ballot, err := s.polls.Ballot(ctx, eventID, u1.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{a, b}, ballot)

	tallies, voters, err := s.polls.Tallies(ctx, eventID)
	require.NoError(t, err)
	assert.Equal(t, 2, voters)
	byID := map[int64]polls.CandidateTally{}
	for _, c := range tallies {
		byID[c.PersonID] = c
	}
	assert.Equal(t, 2, byID[a].FirstChoices)
	assert.Equal(t, 2, byID[a].Chosen)
	assert.Equal(t, 0, byID[b].FirstChoices)
	assert.Equal(t, 1, byID[b].Chosen)

	// Removing a: u1's ballot now counts b first; u2's ballot is empty.
	removed, err := s.polls.RemoveCandidate(ctx, eventID, a)
	require.NoError(t, err)
	assert.True(t, removed)
	tallies, voters, err = s.polls.Tallies(ctx, eventID)
	require.NoError(t, err)
	assert.Equal(t, 1, voters)
	require.Len(t, tallies, 1)
	assert.Equal(t, 1, tallies[0].FirstChoices)

	n, err := s.acc.PurgeUsers(ctx, "noop")
	require.NoError(t, err)
	assert.Equal(t, int64(1), n)
	_, voters, err = s.polls.Tallies(ctx, eventID)
	require.NoError(t, err)
	assert.Equal(t, 0, voters)
	assert.Equal(t, 4, s.updatesSince(t, eventID, firstEvent), "one event per saved ballot, one for the purge")
}

// TestConcurrentVotes: the votes one user casts at once (two tabs) apply one
// after the other, so the ballot never holds more than the votes allowed,
// and erasing the user drops their ballot and refreshes the pages.
func TestConcurrentVotes(t *testing.T) {
	s := openStores(t)
	ctx := context.Background()
	eventID, err := s.polls.SaveEvent(ctx, polls.Event{Slug: "race-poll", TitleFR: "Race", TitleEN: "Race",
		MaxChoices: 3, MinVoters: 1, OpensAt: time.Now().Add(-time.Hour)})
	require.NoError(t, err)
	var candidates []int64
	for _, name := range []string{"race a", "race b", "race c", "race d", "race e", "race f"} {
		id, err := s.storer.FindOrInsertPersonWithType(ctx, name, "human")
		require.NoError(t, err)
		_, err = s.polls.AddCandidate(ctx, eventID, id)
		require.NoError(t, err)
		candidates = append(candidates, id)
	}
	u, err := s.acc.UpsertUser(ctx, "noop", "racer", "Racer")
	require.NoError(t, err)
	_, err = s.acc.AttachVerification(ctx, u.ID, "noop:racer")
	require.NoError(t, err)

	full := errors.New("full")
	var wg sync.WaitGroup
	errs := make(chan error, len(candidates))
	for _, id := range candidates {
		wg.Add(1)
		go func(id int64) {
			defer wg.Done()
			_, err := s.polls.UpdateBallot(ctx, eventID, u.ID, true, func(current []int64) ([]int64, error) {
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
	ballot, err := s.polls.Ballot(ctx, eventID, u.ID)
	require.NoError(t, err)
	assert.Len(t, ballot, 3)

	choices, err := s.polls.UserChoices(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, choices, 3)
	for i, c := range choices {
		assert.Equal(t, ballot[i], c.PersonID)
		assert.Equal(t, i+1, c.Rank)
	}

	before := s.lastEvent(t)
	require.NoError(t, s.acc.DeleteUser(ctx, u.ID))
	choices, err = s.polls.UserChoices(ctx, u.ID)
	require.NoError(t, err)
	assert.Empty(t, choices)
	assert.Equal(t, 1, s.updatesSince(t, eventID, before))
	_, err = s.polls.UpdateBallot(ctx, eventID, u.ID, true, func(c []int64) ([]int64, error) { return c, nil })
	assert.Error(t, err, "a deleted user has no ballot to change")
}

// TestIdentityMove: a document shown on another account carries its ballots
// and its consent there, and the old account can no longer vote.
func TestIdentityMove(t *testing.T) {
	s := openStores(t)
	ctx := context.Background()
	eventID, err := s.polls.SaveEvent(ctx, polls.Event{Slug: "move-poll", TitleFR: "Move", TitleEN: "Move",
		MaxChoices: 2, MinVoters: 1, OpensAt: time.Now().Add(-time.Hour)})
	require.NoError(t, err)
	a, err := s.storer.FindOrInsertPersonWithType(ctx, "move candidate", "human")
	require.NoError(t, err)
	_, err = s.polls.AddCandidate(ctx, eventID, a)
	require.NoError(t, err)

	old, err := s.acc.UpsertUser(ctx, "google", "old", "")
	require.NoError(t, err)
	from, err := s.acc.AttachVerification(ctx, old.ID, "zkpassport:doc")
	require.NoError(t, err)
	assert.Zero(t, from)
	_, err = s.polls.UpdateBallot(ctx, eventID, old.ID, true, set(a))
	require.NoError(t, err)

	fresh, err := s.acc.UpsertUser(ctx, "google", "new", "")
	require.NoError(t, err)
	before := s.lastEvent(t)
	from, err = s.acc.AttachVerification(ctx, fresh.ID, "zkpassport:doc")
	require.NoError(t, err)
	assert.Equal(t, old.ID, from)
	ballot, err := s.polls.Ballot(ctx, eventID, fresh.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{a}, ballot)
	consented, err := s.polls.Consented(ctx, fresh.ID)
	require.NoError(t, err)
	assert.True(t, consented)
	ballot, err = s.polls.Ballot(ctx, eventID, old.ID)
	require.NoError(t, err)
	assert.Empty(t, ballot)
	_, err = s.polls.UpdateBallot(ctx, eventID, old.ID, true, set(a))
	assert.ErrorIs(t, err, polls.ErrNotVerified)
	assert.Equal(t, 1, s.updatesSince(t, eventID, before), "the open pages refresh")
	_, voters, err := s.polls.Tallies(ctx, eventID)
	require.NoError(t, err)
	assert.Equal(t, 1, voters)
}
