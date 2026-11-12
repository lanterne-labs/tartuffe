package seatreserve

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCheck: a seat over the line is held until its window resets; a
// reading is made once per Every; under the line, on a failure or without a
// reserve there is no hold.
func TestCheck(t *testing.T) {
	ctx := context.Background()
	reset := time.Now().Add(3 * time.Hour)
	reads := 0
	r := New(90)
	r.Read = func(_ context.Context, token string) (Usage, error) {
		reads++
		assert.Equal(t, "tok", token)
		return Usage{FiveHour: &Window{Percent: 27}, SevenDay: &Window{Percent: 91, Reset: reset}}, nil
	}
	assert.Equal(t, reset.Add(resetMargin), r.Check(ctx, "bob", "tok"))
	assert.True(t, r.Check(ctx, "bob", "tok").IsZero(), "no second reading within Every")
	assert.Equal(t, 1, reads)

	below := New(90)
	below.Read = func(context.Context, string) (Usage, error) {
		return Usage{FiveHour: &Window{Percent: 89.9}, SevenDay: &Window{Percent: 40}}, nil
	}
	assert.True(t, below.Check(ctx, "bob", "tok").IsZero())

	failed := New(90)
	failed.Read = func(context.Context, string) (Usage, error) { return Usage{}, errors.New("no network") }
	assert.True(t, failed.Check(ctx, "bob", "tok").IsZero())

	off := New(0)
	off.Read = func(context.Context, string) (Usage, error) { t.Fatal("no reserve: no reading"); return Usage{}, nil }
	assert.True(t, off.Check(ctx, "bob", "tok").IsZero())
	var none *Reserve
	assert.True(t, none.Check(ctx, "bob", "tok").IsZero())
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
