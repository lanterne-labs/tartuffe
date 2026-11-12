package llm

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

func reserveEndpoint(usage seatUsage, err error) (*ClaudeTeamEndpoint, *int) {
	probes := 0
	e := &ClaudeTeamEndpoint{Binary: "/nonexistent", ProcsPerSeat: 1, ReservePercent: 90,
		seats: []claudeSeat{{name: "alice", token: "tok-alice"}, {name: "bob", token: "tok-bob"}}}
	e.probe = func(_ context.Context, token string) (seatUsage, error) {
		probes++
		if token != "tok-bob" {
			return seatUsage{}, errors.New("the reserve is the last seat")
		}
		return usage, err
	}
	return e, &probes
}

// TestReserve_HoldsTheLastSeat: at 90 % of a window the last seat leaves the
// pipeline until that window resets, and still serves the reserved tasks.
func TestReserve_HoldsTheLastSeat(t *testing.T) {
	ctx := context.Background()
	reset := time.Now().Add(3 * time.Hour)
	e, probes := reserveEndpoint(seatUsage{FiveHour: &usageWindow{Percent: 27}, SevenDay: &usageWindow{Percent: 91, Reset: reset}}, nil)
	e.checkReserve(ctx)
	e.checkReserve(ctx)
	assert.Equal(t, 1, *probes, "one reading per minute, none while the seat is held")

	seat, release, _, err := e.acquire(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, "alice", seat.name)
	// alice is full: the pipeline waits rather than take the reserve.
	wait, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, _, _, err = e.acquire(wait, false)
	cancel()
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	// The bot takes it.
	seat, releaseBot, _, err := e.acquire(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, "bob", seat.name)
	releaseBot()
	release()

	// alice at her limit: the pipeline has no seat (the worker waits), the bot has one.
	e.seatDown("alice", time.Now().Add(time.Hour), time.Now().Add(time.Hour), time.Now())
	assert.False(t, e.Serving())
	_, release, earliest, err := e.acquire(ctx, false)
	require.NoError(t, err)
	assert.Nil(t, release)
	assert.Equal(t, "alice", earliest.name, "the first seat back")
	_, ok := e.serving(time.Now(), true)
	assert.True(t, ok)
}

// TestReserve_BelowTheLine: under 90 % everyone uses the seat, and a failed
// reading keeps no reserve.
func TestReserve_BelowTheLine(t *testing.T) {
	ctx := context.Background()
	for name, e := range map[string]*ClaudeTeamEndpoint{
		"below":  first(reserveEndpoint(seatUsage{FiveHour: &usageWindow{Percent: 89.9}, SevenDay: &usageWindow{Percent: 40}}, nil)),
		"failed": first(reserveEndpoint(seatUsage{}, errors.New("no network"))),
	} {
		e.checkReserve(ctx)
		_, release, _, err := e.acquire(ctx, false)
		require.NoError(t, err, name)
		defer release()
		seat, release2, _, err := e.acquire(ctx, false)
		require.NoError(t, err, name)
		assert.Equal(t, "bob", seat.name, name)
		release2()
	}
	off, probes := reserveEndpoint(seatUsage{FiveHour: &usageWindow{Percent: 99}}, nil)
	off.ReservePercent = 0
	off.checkReserve(ctx)
	assert.Equal(t, 0, *probes, "no reserve: no reading")
}

func first(e *ClaudeTeamEndpoint, _ *int) *ClaudeTeamEndpoint { return e }

func TestSeatUsage_HoldUntil(t *testing.T) {
	now := time.Now()
	five, seven := now.Add(2*time.Hour), now.Add(48*time.Hour)
	u := seatUsage{FiveHour: &usageWindow{Percent: 95, Reset: five}, SevenDay: &usageWindow{Percent: 60, Reset: seven}}
	assert.Equal(t, five.Add(claudeResetMargin), u.holdUntil(90, now), "the window over the line decides")
	u.SevenDay.Percent = 90
	assert.Equal(t, seven.Add(claudeResetMargin), u.holdUntil(90, now), "the latest reset of the windows over the line")
	assert.Equal(t, now.Add(reserveHoldUnknown), seatUsage{FiveHour: &usageWindow{Percent: 100}}.holdUntil(90, now), "no reset given")
	assert.True(t, seatUsage{FiveHour: &usageWindow{Percent: 10}}.holdUntil(90, now).IsZero())
}

// TestProbeSeatUsage: the usage comes from the headers, of a 429 too, with
// the seat's token as the bearer.
func TestProbeSeatUsage(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "Bearer tok-bob", r.Header.Get("Authorization"))
		w.Header().Set("anthropic-ratelimit-unified-5h-utilization", "0.27")
		w.Header().Set("anthropic-ratelimit-unified-5h-reset", "1794507000")
		w.Header().Set("anthropic-ratelimit-unified-7d-utilization", "0.85")
		w.WriteHeader(status)
	}))
	defer srv.Close()
	old := reserveProbeURL
	reserveProbeURL = srv.URL
	defer func() { reserveProbeURL = old }()
	for _, status = range []int{http.StatusOK, http.StatusTooManyRequests} {
		u, err := probeSeatUsage(context.Background(), "tok-bob")
		require.NoError(t, err)
		require.NotNil(t, u.FiveHour)
		assert.InDelta(t, 27, u.FiveHour.Percent, 0.001)
		assert.Equal(t, int64(1794507000), u.FiveHour.Reset.Unix())
		require.NotNil(t, u.SevenDay)
		assert.InDelta(t, 85, u.SevenDay.Percent, 0.001)
		assert.True(t, u.SevenDay.Reset.IsZero())
	}
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusUnauthorized) }))
	defer srv2.Close()
	reserveProbeURL = srv2.URL
	_, err := probeSeatUsage(context.Background(), "tok-bob")
	assert.ErrorContains(t, err, "HTTP 401")
	assert.NotContains(t, err.Error(), "tok-bob")
}
