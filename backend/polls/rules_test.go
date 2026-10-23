package polls

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func event() *Event {
	closes := now.Add(24 * time.Hour)
	return &Event{Slug: "e", MaxChoices: 3, MinVoters: 2, OpensAt: now.Add(-time.Hour), ClosesAt: &closes}
}

// sampleTallies counts 20 voters: B has the most first choices, but A
// beats each other candidate head to head.
func sampleTallies() []CandidateTally {
	t := NewTally([]int64{1, 2, 3, 4})
	add := func(n int, kind string, choices ...int64) {
		for range n {
			t.Add(kind, choices)
		}
	}
	add(7, KindChoices, 2)
	add(5, KindChoices, 1, 3)
	add(5, KindChoices, 3, 1)
	add(2, KindChoices, 4, 1)
	add(1, KindBlank)
	add(1, KindWithdraw)
	out, _ := t.Result()
	cards := map[int64]Card{1: {Name: "Alice", HonestyScore: 0.58}, 2: {Name: "Bruno", HonestyScore: 0.81}, 3: {Name: "Chloé", HonestyScore: 0.58}, 4: {Name: "David", HonestyScore: 0.30}}
	for i := range out {
		out[i].Card = cards[out[i].PersonID]
	}
	return out
}

func TestTally(t *testing.T) {
	tallies := sampleTallies()
	byID := map[int64]CandidateTally{}
	for _, c := range tallies {
		byID[c.PersonID] = c
	}
	assert.Equal(t, 7, byID[2].FirstChoices)
	assert.Equal(t, 12, byID[1].Ranked)
	assert.Equal(t, 12, byID[1].Beats[2], "every ballot ranking Alice and not Bruno")
	assert.Equal(t, 7, byID[2].Beats[1])
	assert.Equal(t, 7, byID[1].Beats[3])
	assert.Equal(t, 5, byID[3].Beats[1])

	tally := NewTally([]int64{1, 2})
	tally.Add(KindChoices, []int64{9, 2})
	tally.Add(KindChoices, []int64{9})
	out, turnout := tally.Result()
	assert.Equal(t, Turnout{Voters: 1}, turnout, "a ranking of no candidate left is not counted")
	assert.Equal(t, 1, out[1].FirstChoices, "the candidate who left is skipped, the next one moves up")
	assert.Equal(t, 1, out[1].Beats[1])
}

// TestComputeRanks: the preference order is Schulze's, the majority order
// counts first choices, honesty orders by score. Equal values share a rank.
func TestComputeRanks(t *testing.T) {
	r := Compute(event(), sampleTallies(), Turnout{Voters: 20, Blank: 1}, now)
	require.Empty(t, r.Hidden)
	assert.True(t, r.Open)
	require.Len(t, r.Candidates, 4)

	names := []string{}
	for _, c := range r.Candidates {
		names = append(names, c.Name)
	}
	assert.Equal(t, []string{"Alice", "Chloé", "Bruno", "David"}, names, "Schulze order")
	require.NotNil(t, r.CondorcetWinner)
	assert.Equal(t, int64(1), *r.CondorcetWinner)

	ranks := map[string][3]int{}
	for _, c := range r.Candidates {
		ranks[c.Name] = [3]int{*c.PreferenceRank, *c.MajorityRank, c.HonestyRank}
	}
	assert.Equal(t, [3]int{1, 2, 2}, ranks["Alice"])
	assert.Equal(t, [3]int{3, 1, 1}, ranks["Bruno"], "most first choices")
	assert.Equal(t, [3]int{2, 2, 2}, ranks["Chloé"], "as many first choices as Alice; same score as Alice")
	assert.Equal(t, [3]int{4, 4, 4}, ranks["David"])

	assert.InDelta(t, 0.25, *r.Candidates[0].FirstChoiceShare, 1e-9)
	assert.InDelta(t, 0.6, *r.Candidates[0].RankedShare, 1e-9)
}

// TestSchulzeCycle: A > B > C > A with equal strength: no winner, one rank.
func TestSchulzeCycle(t *testing.T) {
	tally := NewTally([]int64{1, 2, 3})
	tally.Add(KindChoices, []int64{1, 2, 3})
	tally.Add(KindChoices, []int64{2, 3, 1})
	tally.Add(KindChoices, []int64{3, 1, 2})
	tallies, turnout := tally.Result()
	r := Compute(event(), tallies, turnout, now)
	assert.Nil(t, r.CondorcetWinner)
	for _, c := range r.Candidates {
		assert.Equal(t, 1, *c.PreferenceRank, c.PersonID)
	}

	// A stronger link breaks the cycle: 2 voters more for A > B > C.
	tally.Add(KindChoices, []int64{1, 2, 3})
	tally.Add(KindChoices, []int64{1, 2, 3})
	tallies, turnout = tally.Result()
	r = Compute(event(), tallies, turnout, now)
	require.NotNil(t, r.CondorcetWinner)
	assert.Equal(t, int64(1), r.Candidates[0].PersonID)
}

// TestComputeHidden: under min_voters or in a blackout no count, share or
// preference rank leaves the server; the honesty ranks still do.
func TestComputeHidden(t *testing.T) {
	r := Compute(event(), sampleTallies(), Turnout{Voters: 1}, now)
	assert.Equal(t, HiddenTooFewVoters, r.Hidden)
	assert.Equal(t, 1, r.Voters)

	e := event()
	e.Blackouts = []Blackout{{StartsAt: now.Add(-time.Minute), EndsAt: now.Add(time.Minute)}}
	r = Compute(e, sampleTallies(), Turnout{Voters: 4}, now)
	assert.Equal(t, HiddenBlackout, r.Hidden)
	assert.Equal(t, "Bruno", r.Candidates[0].Name, "by honesty while hidden")
	for _, c := range r.Candidates {
		assert.Nil(t, c.FirstChoices)
		assert.Nil(t, c.FirstChoiceShare)
		assert.Nil(t, c.RankedShare)
		assert.Nil(t, c.PreferenceRank)
		assert.Nil(t, c.MajorityRank)
		assert.NotZero(t, c.HonestyRank)
	}

	e.MinVoters = 0
	e.Blackouts = nil
	assert.Equal(t, HiddenTooFewVoters, Compute(e, sampleTallies(), Turnout{Voters: 0}, now).Hidden, "no ballot at all: nothing to share")
}

func TestCheckBallot(t *testing.T) {
	e := event()
	candidates := []int64{1, 2, 3, 4}
	assert.NoError(t, CheckBallot(e, candidates, []int64{2, 1}, now))
	assert.NoError(t, CheckBallot(e, candidates, []int64{4, 1, 3}, now))

	for name, ranked := range map[string][]int64{
		"too many":      {1, 2, 3, 4},
		"not candidate": {9},
		"twice":         {1, 1},
	} {
		err := CheckBallot(e, candidates, ranked, now)
		assert.True(t, errors.Is(err, ErrBallot), name)
	}

	assert.ErrorIs(t, CheckBallot(e, candidates, []int64{1}, e.ClosesAt.Add(time.Second)), ErrClosed)
	assert.ErrorIs(t, CheckBallot(e, candidates, []int64{1}, e.OpensAt.Add(-time.Second)), ErrClosed)
}

// TestComputeBlank: a blank vote counts in the voters and has its share.
func TestComputeBlank(t *testing.T) {
	r := Compute(event(), sampleTallies(), Turnout{Voters: 5, Blank: 1}, time.Now())
	require.NotNil(t, r.Blank)
	assert.Equal(t, 1, *r.Blank)
	assert.InDelta(t, 0.2, *r.BlankShare, 1e-9)
	hidden := Compute(event(), sampleTallies(), Turnout{Voters: 0}, time.Now())
	assert.Nil(t, hidden.Blank, "hidden with the shares")
}

// TestAnonBallotMessage: the browser builds the same text
// (frontend/src/lib/anonballot.test.ts).
func TestAnonBallotMessage(t *testing.T) {
	w := Window{Series: 1, Index: 9}
	b := AnonBallot{EventID: 12, Window: w, Seq: 3, Kind: KindChoices, Choices: []int64{7, 4, 9}}
	assert.Equal(t, "tartuffe-ballot/v1;poll=12;series=1;window=9;seq=3;choices=7,4,9", b.Message())
	b = AnonBallot{EventID: 12, Window: w, Seq: 1, Kind: KindBlank}
	assert.Equal(t, "tartuffe-ballot/v1;poll=12;series=1;window=9;seq=1;blank", b.Message())
	assert.Equal(t, "tartuffe/ballot/v1/12/1/9", ScopeName(12, w))
}
