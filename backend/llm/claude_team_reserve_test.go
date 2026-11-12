package llm

import (
	"context"
	"testing"
	"time"

	"digital-mirror/backend/llm/seatreserve"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClaudeTeamEndpoint_Reserve: once the reserve holds the last seat, the
// pipeline leaves it (it waits for another seat, and has none when the
// others are down) while a reserved task still takes it. Only the last seat
// is read.
func TestClaudeTeamEndpoint_Reserve(t *testing.T) {
	ctx := context.Background()
	reserve := seatreserve.New(90)
	reserve.Read = func(_ context.Context, token string) (seatreserve.Usage, error) {
		assert.Equal(t, "tok-bob", token)
		return seatreserve.Usage{SevenDay: &seatreserve.Window{Percent: 91, Reset: time.Now().Add(3 * time.Hour)}}, nil
	}
	e := &ClaudeTeamEndpoint{Binary: "/nonexistent", ProcsPerSeat: 1, Reserve: reserve,
		seats: []claudeSeat{{name: "alice", token: "tok-alice"}, {name: "bob", token: "tok-bob"}}}
	e.holdReserve(ctx)

	seat, release, _, err := e.acquire(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, "alice", seat.name)
	wait, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, _, _, err = e.acquire(wait, false)
	cancel()
	assert.ErrorIs(t, err, context.DeadlineExceeded, "alice is full: the pipeline waits rather than take the reserve")
	seat, releaseBot, _, err := e.acquire(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, "bob", seat.name)
	releaseBot()
	release()

	e.seatDown("alice", time.Now().Add(time.Hour), time.Now().Add(time.Hour), time.Now())
	assert.False(t, e.Serving(), "no seat for the pipeline")
	_, release, earliest, err := e.acquire(ctx, false)
	require.NoError(t, err)
	assert.Nil(t, release)
	assert.Equal(t, "alice", earliest.name, "the first seat back")
	_, ok := e.serving(time.Now(), true)
	assert.True(t, ok, "the bot has its seat")

	assert.True(t, TaskTweet.Reserved())
	assert.False(t, TaskExplain.Reserved())
}
