package seatreserve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestHold: nothing is kept while a seat that serves has room; once none
// has, the seat with the most left is kept until its window resets, and the
// others go on; a seat with room again ends the hold. A seat is read once
// per Every; an unknown usage or no reserve keeps nothing.
func TestHold(t *testing.T) {
	ctx := context.Background()
	reset := time.Now().Add(3 * time.Hour)
	var mu sync.Mutex
	used := map[string]float64{"tok-a": 99, "tok-b": 50, "tok-c": 95}
	reads := 0
	r := New(90)
	r.Every = time.Hour
	r.Read = func(_ context.Context, token string) (Usage, error) {
		mu.Lock()
		defer mu.Unlock()
		reads++
		return Usage{FiveHour: &Window{Percent: 10}, SevenDay: &Window{Percent: used[token], Reset: reset}}, nil
	}
	seats := []Seat{{"a", "tok-a"}, {"b", "tok-b"}, {"c", "tok-c"}}
	name, until := r.Hold(ctx, seats)
	assert.Equal(t, "", name, "b has room: a and c may go to their end")
	assert.True(t, until.IsZero())
	name, _ = r.Hold(ctx, seats)
	assert.Equal(t, "", name)
	assert.Equal(t, 3, reads, "one reading per seat and Every")

	// b reached its limit and left the seats that serve: c has the most left.
	name, until = r.Hold(ctx, []Seat{{"a", "tok-a"}, {"c", "tok-c"}})
	assert.Equal(t, "c", name)
	assert.Equal(t, reset.Add(resetMargin), until)

	// b is back with room: nothing is kept.
	name, _ = r.Hold(ctx, seats)
	assert.Equal(t, "", name)

	failed := New(90)
	failed.Read = func(context.Context, string) (Usage, error) { return Usage{}, errors.New("no network") }
	name, _ = failed.Hold(ctx, seats[:1])
	assert.Equal(t, "", name, "usage not known: nothing kept")

	off := New(0)
	off.Read = func(context.Context, string) (Usage, error) { t.Error("no reserve: no reading"); return Usage{}, nil }
	name, _ = off.Hold(ctx, seats)
	assert.Equal(t, "", name)
	var none *Reserve
	name, _ = none.Hold(ctx, seats)
	assert.Equal(t, "", name)
}

func TestHoldUntil(t *testing.T) {
	now := time.Now()
	five, seven := now.Add(2*time.Hour), now.Add(48*time.Hour)
	u := Usage{FiveHour: &Window{Percent: 95, Reset: five}, SevenDay: &Window{Percent: 60, Reset: seven}}
	assert.Equal(t, five.Add(resetMargin), u.HoldUntil(90, now), "the window over the line decides")
	u.SevenDay.Percent = 90
	assert.Equal(t, seven.Add(resetMargin), u.HoldUntil(90, now), "the latest reset of the windows over the line")
	assert.Equal(t, now.Add(holdUnknown), Usage{FiveHour: &Window{Percent: 100}}.HoldUntil(90, now), "no reset given")
	assert.True(t, Usage{FiveHour: &Window{Percent: 10}}.HoldUntil(90, now).IsZero())
}

// TestRead: the usage comes from the headers, of a 429 too, with the seat's
// token as the bearer; an answer without them is an error without the token.
func TestRead(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok-bob", r.Header.Get("Authorization"))
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.27")
		w.Header().Set("anthropic-ratelimit-unified-5h-reset", "1794507000")
		w.Header().Set("anthropic-ratelimit-unified-7d-utilization", "0.85")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	old := readURL
	defer func() { readURL = old }()
	readURL = srv.URL
	for _, status = range []int{http.StatusOK, http.StatusTooManyRequests} {
		u, err := Read(context.Background(), "tok-bob")
		require.NoError(t, err)
		require.NotNil(t, u.FiveHour)
		assert.InDelta(t, 27, u.FiveHour.Percent, 0.001)
		assert.Equal(t, int64(1794507000), u.FiveHour.Reset.Unix())
		require.NotNil(t, u.SevenDay)
		assert.InDelta(t, 85, u.SevenDay.Percent, 0.001)
		assert.True(t, u.SevenDay.Reset.IsZero())
	}
	bare := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer bare.Close()
	readURL = bare.URL
	_, err := Read(context.Background(), "tok-bob")
	assert.ErrorContains(t, err, "HTTP 401")
	assert.NotContains(t, err.Error(), "tok-bob")
}
