package twitter

import (
	"context"
	"os"
	"testing"
	"time"

	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setupTestDB connects to the test database (no person gate: fixture names
// are synthetic) and wipes the person-scoped tables.
// Like the storage suite, the repo .env wins over the ambient environment when
// present; only DATABASE_URL is taken from it so the live-API tests keep their
// own key gating.
func setupTestDB(t *testing.T) *storage.PostgresStorer {
	t.Helper()
	if env, err := godotenv.Read("../../../.env"); err == nil && env["DATABASE_URL"] != "" {
		t.Setenv("DATABASE_URL", env["DATABASE_URL"])
	}
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set, skipping twitter store tests")
	}
	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err, "connect to test database")
	t.Cleanup(storer.Close)

	_, err = storer.Pool().Exec(ctx, "TRUNCATE TABLE twitter_fetch_log, twitter_handles, persons RESTART IDENTITY CASCADE")
	require.NoError(t, err, "truncate tables")
	return storer
}

func handleCount(ctx context.Context, t *testing.T, storer *storage.PostgresStorer, personID int64, handle string) int {
	t.Helper()
	var n int
	require.NoError(t, storer.Pool().QueryRow(ctx,
		`SELECT count(*) FROM twitter_handles WHERE person_id=$1 AND handle=$2`, personID, handle).Scan(&n))
	return n
}

// TestMergePersonsRepointsHandles proves the registry hook: merging a person
// moves its handles to the survivor, and a handle both persons had is dropped
// rather than violating the (person_id, handle) unique constraint.
func TestMergePersonsRepointsHandles(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	store := NewStore(storer)

	intoID, err := storer.FindOrInsertPersonWithType(ctx, "alice into", "human")
	require.NoError(t, err)
	fromID, err := storer.FindOrInsertPersonWithType(ctx, "alice from", "human")
	require.NoError(t, err)

	require.NoError(t, store.AddHandle(ctx, fromID, "alicefrom"))
	require.NoError(t, store.AddHandle(ctx, fromID, "shared"))
	require.NoError(t, store.AddHandle(ctx, intoID, "shared"))

	require.NoError(t, storer.MergePersons(ctx, fromID, intoID))

	assert.Equal(t, 1, handleCount(ctx, t, storer, intoID, "alicefrom"), "from's handle should follow the survivor")
	assert.Equal(t, 0, handleCount(ctx, t, storer, fromID, "alicefrom"), "no handle should still reference from")
	assert.Equal(t, 1, handleCount(ctx, t, storer, intoID, "shared"), "colliding handle is kept once on the survivor")

	handles, err := store.GetHandlesByPersonID(ctx, intoID)
	require.NoError(t, err)
	assert.Len(t, handles, 2, "survivor ends up with exactly the two distinct handles")
}

// TestDeleteFactsForPersonResetsFetchLog proves the registry's reset hook:
// deleting a person's facts clears the fetch log of their handles so the next
// run re-fetches the ranges, while the handles themselves survive.
func TestDeleteFactsForPersonResetsFetchLog(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	store := NewStore(storer)

	personID, err := storer.FindOrInsertPersonWithType(ctx, "bob tweets", "human")
	require.NoError(t, err)
	require.NoError(t, store.AddHandle(ctx, personID, "bobtweets"))
	handles, err := store.GetHandlesByPersonID(ctx, personID)
	require.NoError(t, err)
	require.Len(t, handles, 1)
	handleID := handles[0].ID

	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	to := time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, store.LogFetch(ctx, handleID, from, to, 3))
	fetched, err := store.IsRangeFetched(ctx, handleID, from, to)
	require.NoError(t, err)
	require.True(t, fetched, "range should be logged before the reset")

	_, err = storer.DeleteFactsForPerson(ctx, personID)
	require.NoError(t, err)

	fetched, err = store.IsRangeFetched(ctx, handleID, from, to)
	require.NoError(t, err)
	assert.False(t, fetched, "fetch log should be cleared when the person's facts are deleted")

	handles, err = store.GetHandlesByPersonID(ctx, personID)
	require.NoError(t, err)
	assert.Len(t, handles, 1, "handles survive a fact deletion")
}

// TestLastFetchedOnlyMovesForward: reading an older range (the history step)
// must not move last_fetched_at, where the daily fetch starts, past tweets it
// has not read yet, nor back.
func TestLastFetchedOnlyMovesForward(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	store := NewStore(storer)

	pid, err := storer.FindOrInsertPersonWithType(ctx, "bob history", "human")
	require.NoError(t, err)
	require.NoError(t, store.AddHandle(ctx, pid, "bobhistory"))
	handles, err := store.GetHandlesByPersonID(ctx, pid)
	require.NoError(t, err)
	id := handles[0].ID

	daily := time.Date(2026, 10, 1, 4, 0, 0, 0, time.UTC)
	require.NoError(t, store.UpdateHandleLastFetched(ctx, id, daily))
	require.NoError(t, store.UpdateHandleLastFetched(ctx, id, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)))

	handles, err = store.GetHandlesByPersonID(ctx, pid)
	require.NoError(t, err)
	require.NotNil(t, handles[0].LastFetchedAt)
	assert.True(t, handles[0].LastFetchedAt.Equal(daily), "an older range left last_fetched_at at %v, want %v", handles[0].LastFetchedAt, daily)
}

// TestHandlesFetchedCover: a stretch is fetched once every day of it is in the
// fetch log of every active handle of the persons; a person without a handle
// has nothing to fetch.
func TestHandlesFetchedCover(t *testing.T) {
	storer := setupTestDB(t)
	ctx := context.Background()
	store := NewStore(storer)
	pid, err := storer.FindOrInsertPersonWithType(ctx, "carol cover", "human")
	require.NoError(t, err)
	noHandle, err := storer.FindOrInsertPersonWithType(ctx, "dan cover", "human")
	require.NoError(t, err)
	require.NoError(t, store.AddHandle(ctx, pid, "carolcover"))
	handles, err := store.GetHandlesByPersonID(ctx, pid)
	require.NoError(t, err)
	d := func(s string) time.Time { t, _ := time.Parse("2006-01-02", s); return t }

	require.NoError(t, store.LogFetch(ctx, handles[0].ID, d("2026-09-01"), d("2026-09-04"), 12))
	covered, err := store.HandlesFetchedCover(ctx, []int64{pid, noHandle}, d("2026-09-01"), d("2026-09-04"))
	require.NoError(t, err)
	assert.True(t, covered)

	covered, err = store.HandlesFetchedCover(ctx, []int64{pid}, d("2026-09-01"), d("2026-09-05"))
	require.NoError(t, err)
	assert.False(t, covered, "2026-09-04 was never fetched")
}
