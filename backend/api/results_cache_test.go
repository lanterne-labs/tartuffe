package api

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResultsCacheSharesOneQuery: the requests that arrive together, as
// every open page refetching after a vote, wait on one query.
func TestResultsCacheSharesOneQuery(t *testing.T) {
	c := newResultsCache(time.Minute)
	var calls atomic.Int32
	release := make(chan struct{})
	load := func(context.Context, int64) ([]storage.PollCandidateTally, int, error) {
		calls.Add(1)
		<-release
		return nil, 7, nil
	}
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, voters, err := c.get(context.Background(), 1, load)
			assert.NoError(t, err)
			assert.Equal(t, 7, voters)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.Equal(t, int32(1), calls.Load())

	_, _, err := c.get(context.Background(), 1, load)
	require.NoError(t, err)
	assert.Equal(t, int32(1), calls.Load(), "served from the cache")
}

// TestResultsCacheInvalidate: a ballot change drops the counts at once, and
// a query that started before it does not store its stale counts.
func TestResultsCacheInvalidate(t *testing.T) {
	c := newResultsCache(time.Minute)
	voters := 1
	load := func(context.Context, int64) ([]storage.PollCandidateTally, int, error) { return nil, voters, nil }
	_, got, _ := c.get(context.Background(), 1, load)
	assert.Equal(t, 1, got)

	voters = 2
	_, got, _ = c.get(context.Background(), 1, load)
	assert.Equal(t, 1, got, "cached")
	c.invalidate(1)
	_, got, _ = c.get(context.Background(), 1, load)
	assert.Equal(t, 2, got)

	voters = 3
	c.invalidate(0)
	_, got, _ = c.get(context.Background(), 1, load)
	assert.Equal(t, 3, got, "invalidating every event")

	// A query in flight while the ballot changes.
	started, release := make(chan struct{}), make(chan struct{})
	slow := func(context.Context, int64) ([]storage.PollCandidateTally, int, error) {
		close(started)
		<-release
		return nil, 3, nil
	}
	c.invalidate(1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.get(context.Background(), 1, slow)
	}()
	<-started
	c.invalidate(1)
	close(release)
	<-done
	voters = 4
	_, got, _ = c.get(context.Background(), 1, load)
	assert.Equal(t, 4, got, "the stale query was not stored")
}

// TestResultsCacheTTL: counts changed elsewhere show once the TTL passes,
// and an error is not cached.
func TestResultsCacheTTL(t *testing.T) {
	c := newResultsCache(20 * time.Millisecond)
	voters := 1
	load := func(context.Context, int64) ([]storage.PollCandidateTally, int, error) { return nil, voters, nil }
	c.get(context.Background(), 1, load)
	voters = 2
	time.Sleep(30 * time.Millisecond)
	_, got, _ := c.get(context.Background(), 1, load)
	assert.Equal(t, 2, got)

	failing := func(context.Context, int64) ([]storage.PollCandidateTally, int, error) {
		return nil, 0, assert.AnError
	}
	_, _, err := c.get(context.Background(), 2, failing)
	assert.ErrorIs(t, err, assert.AnError)
	_, got, err = c.get(context.Background(), 2, load)
	require.NoError(t, err)
	assert.Equal(t, 2, got)
}
