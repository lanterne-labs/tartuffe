// The rules of a poll: its results and the checks of a ballot.
// A ballot ranks up to MaxChoices candidates, from the preferred one. The
// results order the candidates twice from the same ballots: by the Condorcet
// method of Schulze (who beats whom head to head, a ranked candidate above
// every unranked one), and by first choices alone, as the one-round majority
// vote would. Next to them stands each candidate's honesty score; the orders
// are never mixed into one number.
package polls

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Why the shares of an event are not shown.
const (
	HiddenTooFewVoters = "too_few_voters"
	HiddenBlackout     = "blackout"
)

// CandidateResult is one candidate's line of the results. The counts, shares
// and preference rank are nil while the results are hidden.
type CandidateResult struct {
	PersonID           int64    `json:"person_id"`
	Name               string   `json:"name"`
	ImageURL           *string  `json:"image_url,omitempty"`
	HonestyScore       float64  `json:"honesty_score"`
	FactCount          int      `json:"fact_count"`
	ContradictionCount int      `json:"contradiction_count"`
	FirstChoices       *int     `json:"first_choices"`
	FirstChoiceShare   *float64 `json:"first_choice_share"`
	Ranked             *int     `json:"ranked"`
	RankedShare        *float64 `json:"ranked_share"`
	// PreferenceRank is the Schulze rank, MajorityRank the rank by first
	// choices.
	PreferenceRank *int `json:"preference_rank"`
	MajorityRank   *int `json:"majority_rank"`
	HonestyRank    int  `json:"honesty_rank"`
}

// Results is the state of an event at one moment.
type Results struct {
	// Voters counts the ballots, blank votes included: the shares are of
	// them, so a blank vote weighs like any other.
	Voters int `json:"voters"`
	// Blank and BlankShare count the blank votes (nil while hidden).
	Blank      *int     `json:"blank"`
	BlankShare *float64 `json:"blank_share"`
	// Window is the window the results count, and Until when it ends.
	Window Window    `json:"window"`
	Until  time.Time `json:"until"`
	// Open is true while ballots are taken.
	Open bool `json:"open"`
	// Hidden is set (HiddenTooFewVoters, HiddenBlackout) when the shares are
	// not shown.
	Hidden string `json:"hidden,omitempty"`
	// CondorcetWinner is the candidate who beats each other one head to head,
	// if one does (nil while hidden).
	CondorcetWinner *int64 `json:"condorcet_winner"`
	// Candidates are in preference (Schulze) order when shown, else by
	// honesty.
	Candidates []CandidateResult `json:"candidates"`
}

// IsOpen tells whether the event takes ballots at now.
func IsOpen(e *Event, now time.Time) bool {
	return !now.Before(e.OpensAt) && (e.ClosesAt == nil || now.Before(*e.ClosesAt))
}

// InBlackout tells whether the results of the event are under a blackout at now.
func InBlackout(e *Event, now time.Time) bool {
	for _, b := range e.Blackouts {
		if !now.Before(b.StartsAt) && now.Before(b.EndsAt) {
			return true
		}
	}
	return false
}

// Tally counts ballots, the last of each voter: the store counts the
// results with it, and anyone recounts the public log the same way.
type Tally struct {
	index   map[int64]int
	out     []CandidateTally
	turnout Turnout
}

// NewTally starts a count over the candidates of the poll.
func NewTally(candidates []int64) *Tally {
	t := &Tally{index: make(map[int64]int, len(candidates)), out: make([]CandidateTally, len(candidates))}
	for i, id := range candidates {
		t.index[id] = i
		t.out[i] = CandidateTally{PersonID: id, Beats: map[int64]int{}}
	}
	return t
}

// Add counts one ballot. A ranked candidate who left the poll is skipped,
// the next ones move up; a ranking with none left is not counted.
func (t *Tally) Add(kind string, choices []int64) {
	switch kind {
	case KindBlank:
		t.turnout.Voters++
		t.turnout.Blank++
	case KindChoices:
		ranked := make([]int, 0, len(choices))
		seen := make(map[int]bool, len(choices))
		for _, id := range choices {
			if i, ok := t.index[id]; ok && !seen[i] {
				ranked, seen[i] = append(ranked, i), true
			}
		}
		if len(ranked) == 0 {
			return
		}
		t.turnout.Voters++
		t.out[ranked[0]].FirstChoices++
		for k, i := range ranked {
			t.out[i].Ranked++
			for _, j := range ranked[k+1:] {
				t.out[i].Beats[t.out[j].PersonID]++
			}
			for j := range t.out {
				if !seen[j] {
					t.out[i].Beats[t.out[j].PersonID]++
				}
			}
		}
	}
}

// Result returns the counts.
func (t *Tally) Result() ([]CandidateTally, Turnout) {
	return t.out, t.turnout
}

// Compute builds the results from the tallies of the event's candidates and
// its turnout, counted in the results' window at now.
func Compute(e *Event, tallies []CandidateTally, turnout Turnout, now time.Time) Results {
	voters := turnout.Voters
	w := ResultWindow(now)
	r := Results{Voters: voters, Open: IsOpen(e, now), Window: w, Until: w.End(), Candidates: make([]CandidateResult, 0, len(tallies))}
	switch {
	case InBlackout(e, now):
		r.Hidden = HiddenBlackout
	case voters == 0 || voters < e.MinVoters:
		r.Hidden = HiddenTooFewVoters
	}
	for _, t := range tallies {
		c := CandidateResult{
			PersonID: t.PersonID, Name: t.Name, ImageURL: t.ImageURL,
			HonestyScore: t.HonestyScore, FactCount: t.FactCount, ContradictionCount: t.ContradictionCount,
		}
		if r.Hidden == "" {
			first, ranked := t.FirstChoices, t.Ranked
			firstShare, rankedShare := float64(first)/float64(voters), float64(ranked)/float64(voters)
			c.FirstChoices, c.FirstChoiceShare, c.Ranked, c.RankedShare = &first, &firstShare, &ranked, &rankedShare
		}
		r.Candidates = append(r.Candidates, c)
	}

	// Honesty ranks: highest score first; equal scores share a rank.
	byName := func(i, j int) bool {
		return strings.ToLower(r.Candidates[i].Name) < strings.ToLower(r.Candidates[j].Name)
	}
	sort.SliceStable(r.Candidates, func(i, j int) bool {
		if r.Candidates[i].HonestyScore != r.Candidates[j].HonestyScore {
			return r.Candidates[i].HonestyScore > r.Candidates[j].HonestyScore
		}
		return byName(i, j)
	})
	for i := range r.Candidates {
		if i > 0 && r.Candidates[i].HonestyScore == r.Candidates[i-1].HonestyScore {
			r.Candidates[i].HonestyRank = r.Candidates[i-1].HonestyRank
		} else {
			r.Candidates[i].HonestyRank = i + 1
		}
	}
	if r.Hidden != "" {
		return r
	}
	blank, blankShare := turnout.Blank, float64(turnout.Blank)/float64(voters)
	r.Blank, r.BlankShare = &blank, &blankShare

	// Majority ranks: most first choices first; equal counts share a rank.
	sort.SliceStable(r.Candidates, func(i, j int) bool {
		a, b := r.Candidates[i], r.Candidates[j]
		if *a.FirstChoices != *b.FirstChoices {
			return *a.FirstChoices > *b.FirstChoices
		}
		return byName(i, j)
	})
	for i := range r.Candidates {
		rank := i + 1
		if i > 0 && *r.Candidates[i].FirstChoices == *r.Candidates[i-1].FirstChoices {
			rank = *r.Candidates[i-1].MajorityRank
		}
		r.Candidates[i].MajorityRank = &rank
	}

	// Preference ranks: the Schulze order, candidates it ties share a rank
	// and stand by first choices.
	beats := make(map[int64]map[int64]int, len(tallies))
	for _, t := range tallies {
		beats[t.PersonID] = t.Beats
	}
	ids := make([]int64, len(r.Candidates))
	for i, c := range r.Candidates {
		ids[i] = c.PersonID
	}
	wins, winner := Schulze(ids, beats)
	r.CondorcetWinner = winner
	sort.SliceStable(r.Candidates, func(i, j int) bool {
		return wins[r.Candidates[i].PersonID] > wins[r.Candidates[j].PersonID]
	})
	for i := range r.Candidates {
		rank := i + 1
		if i > 0 && wins[r.Candidates[i].PersonID] == wins[r.Candidates[i-1].PersonID] {
			rank = *r.Candidates[i-1].PreferenceRank
		}
		r.Candidates[i].PreferenceRank = &rank
	}
	return r
}

// Schulze runs the Schulze method over the candidates, where beats[a][b]
// counts the voters who rank a above b. It returns, per candidate, how many
// others it beats in the Schulze order (a strict weak order: equal counts
// tie), and the Condorcet winner, the candidate who beats each other one
// head to head, if there is one.
func Schulze(ids []int64, beats map[int64]map[int64]int) (map[int64]int, *int64) {
	n := len(ids)
	d := func(i, j int) int { return beats[ids[i]][ids[j]] }
	// p[i][j] is the strength of the strongest path from i to j.
	p := make([][]int, n)
	for i := range p {
		p[i] = make([]int, n)
		for j := range p[i] {
			if i != j && d(i, j) > d(j, i) {
				p[i][j] = d(i, j)
			}
		}
	}
	for k := 0; k < n; k++ {
		for i := 0; i < n; i++ {
			if i == k {
				continue
			}
			for j := 0; j < n; j++ {
				if j != i && j != k {
					p[i][j] = max(p[i][j], min(p[i][k], p[k][j]))
				}
			}
		}
	}
	wins := make(map[int64]int, n)
	var winner *int64
	for i := 0; i < n; i++ {
		duels := 0
		for j := 0; j < n; j++ {
			if i == j {
				continue
			}
			if p[i][j] > p[j][i] {
				wins[ids[i]]++
			}
			if d(i, j) > d(j, i) {
				duels++
			}
		}
		if n > 1 && duels == n-1 {
			id := ids[i]
			winner = &id
		}
	}
	return wins, winner
}

// ErrBallot is wrapped by every reason a ballot is refused.
var ErrBallot = errors.New("ballot refused")

// ErrClosed refuses a ballot outside the voting period.
var ErrClosed = fmt.Errorf("%w: the event does not take ballots now", ErrBallot)

// CheckBallot checks a ranking against the event and its candidates: at
// most MaxChoices candidates, from the preferred one, each once.
func CheckBallot(e *Event, candidates []int64, ranked []int64, now time.Time) error {
	if !IsOpen(e, now) {
		return ErrClosed
	}
	if len(ranked) > e.MaxChoices {
		return fmt.Errorf("%w: at most %d candidates", ErrBallot, e.MaxChoices)
	}
	isCandidate := make(map[int64]bool, len(candidates))
	for _, id := range candidates {
		isCandidate[id] = true
	}
	seen := make(map[int64]bool, len(ranked))
	for _, id := range ranked {
		if !isCandidate[id] {
			return fmt.Errorf("%w: person %d is not a candidate", ErrBallot, id)
		}
		if seen[id] {
			return fmt.Errorf("%w: person %d is ranked twice", ErrBallot, id)
		}
		seen[id] = true
	}
	return nil
}
