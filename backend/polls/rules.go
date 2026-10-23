// The rules of a poll: its results and the checks of a ballot.
// The results put two numbers side by side for each candidate: the share of
// voters who ranked them first, and their honesty score. Each orders the
// candidates on its own; the two are never mixed into one number.
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
	ChosenShare        *float64 `json:"chosen_share"`
	PreferenceRank     *int     `json:"preference_rank"`
	HonestyRank        int      `json:"honesty_rank"`
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
	// Candidates are in preference order when shown, else by honesty.
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
			first, firstShare, chosenShare := t.FirstChoices, float64(t.FirstChoices)/float64(voters), float64(t.Chosen)/float64(voters)
			c.FirstChoices, c.FirstChoiceShare, c.ChosenShare = &first, &firstShare, &chosenShare
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

	// Preference ranks: most first choices first, then most chosen at all;
	// equal on both share a rank.
	sort.SliceStable(r.Candidates, func(i, j int) bool {
		a, b := r.Candidates[i], r.Candidates[j]
		if *a.FirstChoices != *b.FirstChoices {
			return *a.FirstChoices > *b.FirstChoices
		}
		if *a.ChosenShare != *b.ChosenShare {
			return *a.ChosenShare > *b.ChosenShare
		}
		return byName(i, j)
	})
	for i := range r.Candidates {
		rank := i + 1
		if i > 0 {
			a, b := r.Candidates[i], r.Candidates[i-1]
			if *a.FirstChoices == *b.FirstChoices && *a.ChosenShare == *b.ChosenShare {
				rank = *b.PreferenceRank
			}
		}
		r.Candidates[i].PreferenceRank = &rank
	}
	return r
}

// ErrBallot is wrapped by every reason a ballot is refused.
var ErrBallot = errors.New("ballot refused")

// ErrClosed refuses a ballot outside the voting period.
var ErrClosed = fmt.Errorf("%w: the event does not take ballots now", ErrBallot)

// CheckBallot checks a ballot (first choice first) against the event and its
// candidates. An empty ballot withdraws the user's choices.
func CheckBallot(e *Event, candidates []int64, personIDs []int64, now time.Time) error {
	if !IsOpen(e, now) {
		return ErrClosed
	}
	if len(personIDs) > e.MaxChoices {
		return fmt.Errorf("%w: at most %d choices", ErrBallot, e.MaxChoices)
	}
	isCandidate := make(map[int64]bool, len(candidates))
	for _, id := range candidates {
		isCandidate[id] = true
	}
	seen := map[int64]bool{}
	for _, id := range personIDs {
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
