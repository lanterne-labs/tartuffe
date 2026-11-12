package llm

import (
	"context"
	"testing"
	"time"

	"digital-mirror/backend/llm/seatreserve"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClaudeTeamEndpoint_Reserve: the pipeline uses every seat while one has
// room; when none has, the seat the reserve names is left to the reserved
// tasks (the pipeline waits for another seat, and has none once the others
// are at their limit), whichever seat of the list it is.
func TestClaudeTeamEndpoint_Reserve(t *testing.T) {
	ctx := context.Background()
	used := map[string]float64{"tok-alice": 95, "tok-bob": 50}
	reserve := seatreserve.New(90)
	reserve.Every = time.Nanosecond
	reserve.Read = func(_ context.Context, token string) (seatreserve.Usage, error) {
		return seatreserve.Usage{SevenDay: &seatreserve.Window{Percent: used[token], Reset: time.Now().Add(3 * time.Hour)}}, nil
	}
	e := &ClaudeTeamEndpoint{Binary: "/nonexistent", ProcsPerSeat: 1, Reserve: reserve,
		seats: []claudeSeat{{name: "alice", token: "tok-alice"}, {name: "bob", token: "tok-bob"}}}

	// bob has room: alice may go to her end.
	e.holdReserve(ctx)
	seat, release, _, err := e.acquire(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, "alice", seat.name)
	release()

	// bob passes the line too: alice, with the most left, is kept, the first of the list or not.
	used["tok-bob"] = 97
	e.holdReserve(ctx)
	seat, release, _, err = e.acquire(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, "bob", seat.name, "the pipeline goes on with the other seat")
	wait, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	_, _, _, err = e.acquire(wait, false)
	cancel()
	assert.ErrorIs(t, err, context.DeadlineExceeded, "bob is full: the pipeline waits rather than take the reserve")
	seat, releaseBot, _, err := e.acquire(ctx, true)
	require.NoError(t, err)
	assert.Equal(t, "alice", seat.name, "a reserved task takes it")
	releaseBot()
	release()

	// bob at his limit: the pipeline has no seat, the bot has one.
	e.seatDown("bob", time.Now().Add(time.Hour), time.Now().Add(time.Hour), time.Now())
	e.holdReserve(ctx)
	assert.False(t, e.Serving(), "no seat for the pipeline")
	_, ok := e.serving(time.Now(), true)
	assert.True(t, ok, "the bot has its seat")

	// bob resets with room: alice is free again for the pipeline.
	e.mu.Lock()
	e.seats[1].down, e.seats[1].reset = time.Time{}, time.Time{}
	e.mu.Unlock()
	used["tok-bob"] = 5
	e.holdReserve(ctx)
	seat, release, _, err = e.acquire(ctx, false)
	require.NoError(t, err)
	assert.Equal(t, "alice", seat.name)
	release()

	assert.True(t, TaskTweet.Reserved())
	assert.False(t, TaskExplain.Reserved())
}
