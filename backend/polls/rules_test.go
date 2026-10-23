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

func sampleTallies() []CandidateTally {
	return []CandidateTally{
		{PersonID: 1, Card: Card{Name: "Alice", HonestyScore: 0.58}, FirstChoices: 2, Chosen: 3},
		{PersonID: 2, Card: Card{Name: "Bruno", HonestyScore: 0.81}, FirstChoices: 1, Chosen: 2},
		{PersonID: 3, Card: Card{Name: "Chloé", HonestyScore: 0.58}, FirstChoices: 1, Chosen: 2},
		{PersonID: 4, Card: Card{Name: "David", HonestyScore: 0.30}, FirstChoices: 0, Chosen: 1},
	}
}

// TestComputeRanks: preference orders by first choices, then by how often a
// candidate is chosen at all; honesty by score. Equal values share a rank.
func TestComputeRanks(t *testing.T) {
	r := Compute(event(), sampleTallies(), Turnout{Voters: 4}, now)
	require.Empty(t, r.Hidden)
	assert.True(t, r.Open)
	require.Len(t, r.Candidates, 4)

	names := []string{}
	for _, c := range r.Candidates {
		names = append(names, c.Name)
	}
	assert.Equal(t, []string{"Alice", "Bruno", "Chloé", "David"}, names, "preference order, Bruno and Chloé tie, by name")

	ranks := map[string][2]int{}
	for _, c := range r.Candidates {
		ranks[c.Name] = [2]int{*c.PreferenceRank, c.HonestyRank}
	}
	assert.Equal(t, [2]int{1, 2}, ranks["Alice"])
	assert.Equal(t, [2]int{2, 1}, ranks["Bruno"])
	assert.Equal(t, [2]int{2, 2}, ranks["Chloé"], "same first choices and chosen as Bruno; same score as Alice")
	assert.Equal(t, [2]int{4, 4}, ranks["David"])

	assert.InDelta(t, 0.5, *r.Candidates[0].FirstChoiceShare, 1e-9)
	assert.InDelta(t, 0.75, *r.Candidates[0].ChosenShare, 1e-9)
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
		assert.Nil(t, c.ChosenShare)
		assert.Nil(t, c.PreferenceRank)
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
	assert.NoError(t, CheckBallot(e, candidates, nil, now), "an empty ballot withdraws")

	for name, ballot := range map[string][]int64{
		"too many":      {1, 2, 3, 4},
		"not candidate": {9},
		"twice":         {1, 1},
	} {
		err := CheckBallot(e, candidates, ballot, now)
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
