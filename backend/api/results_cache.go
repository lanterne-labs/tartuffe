package api

import (
	"context"
	"strconv"
	"sync"
	"time"

	"digital-mirror/backend/storage"

	"golang.org/x/sync/singleflight"
)

// resultsTTL bounds how stale the tallies of an event can be: a ballot
// changed on this server drops them at once, one changed elsewhere (another
// replica, a candidate's score updated by the worker) shows within it.
const resultsTTL = 2 * time.Second

// tallies are the counts of one event as GetPollTallies returns them.
type tallies struct {
	candidates []storage.PollCandidateTally
	voters     int
	at         time.Time
}

// resultsCache shares the tallies of each event between the requests: every
// vote makes every open page refetch them (poll_updated), and those requests
// then cost one query instead of one each.
type resultsCache struct {
	ttl   time.Duration
	group singleflight.Group

	mu      sync.Mutex
	entries map[int64]tallies
	// gen counts the invalidations of each event, so a query started
	// before a ballot change does not store its stale counts after it.
	gen    map[int64]uint64
	allGen uint64
}

func newResultsCache(ttl time.Duration) *resultsCache {
	return &resultsCache{ttl: ttl, entries: map[int64]tallies{}, gen: map[int64]uint64{}}
}

// get returns the tallies of the event, querying them with load when the
// cached ones are older than the TTL or were invalidated.
func (c *resultsCache) get(ctx context.Context, eventID int64, load func(context.Context, int64) ([]storage.PollCandidateTally, int, error)) ([]storage.PollCandidateTally, int, error) {
	c.mu.Lock()
	e, ok := c.entries[eventID]
	gen, allGen := c.gen[eventID], c.allGen
	c.mu.Unlock()
	if ok && time.Since(e.at) < c.ttl {
		return e.candidates, e.voters, nil
	}
	key := strconv.FormatInt(eventID, 10) + "/" + strconv.FormatUint(gen, 10) + "/" + strconv.FormatUint(allGen, 10)
	v, err, _ := c.group.Do(key, func() (any, error) {
		// The query outlives the request that started it: the others
		// waiting on it must not fail because that one was cancelled.
		t := tallies{at: time.Now()}
		var err error
		t.candidates, t.voters, err = load(context.WithoutCancel(ctx), eventID)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		if c.gen[eventID] == gen && c.allGen == allGen {
			c.entries[eventID] = t
		}
		c.mu.Unlock()
		return t, nil
	})
	if err != nil {
		return nil, 0, err
	}
	t := v.(tallies)
	return t.candidates, t.voters, nil
}

// invalidate drops the tallies of the event (eventID 0: of every event).
func (c *resultsCache) invalidate(eventID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if eventID == 0 {
		c.allGen++
		clear(c.entries)
		return
	}
	c.gen[eventID]++
	delete(c.entries, eventID)
}
