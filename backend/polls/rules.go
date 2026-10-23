// The rules of a poll: its results and the checks of a ballot.
// A ballot approves the candidates the voter finds acceptable (approval
// voting) and may mark one of them as the favourite. The results put two
// numbers side by side for each candidate: the share of voters who approve
// them, and their honesty score; the favourite share comes second. Each
// orders the candidates on its own; the two are never mixed into one number.
package polls

import (
	"errors"
	"fmt"
	"slices"
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
	Approvals          *int     `json:"approvals"`
	ApprovalShare      *float64 `json:"approval_share"`
	Favourites         *int     `json:"favourites"`
	FavouriteShare     *float64 `json:"favourite_share"`
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
			approvals, favourites := t.Approvals, t.Favourites
			approvalShare, favouriteShare := float64(approvals)/float64(voters), float64(favourites)/float64(voters)
			c.Approvals, c.ApprovalShare, c.Favourites, c.FavouriteShare = &approvals, &approvalShare, &favourites, &favouriteShare
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

	// Preference ranks: most approved first, then most often favourite;
	// equal on both share a rank.
	sort.SliceStable(r.Candidates, func(i, j int) bool {
		a, b := r.Candidates[i], r.Candidates[j]
		if *a.Approvals != *b.Approvals {
			return *a.Approvals > *b.Approvals
		}
		if *a.Favourites != *b.Favourites {
			return *a.Favourites > *b.Favourites
		}
		return byName(i, j)
	})
	for i := range r.Candidates {
		rank := i + 1
		if i > 0 {
			a, b := r.Candidates[i], r.Candidates[i-1]
			if *a.Approvals == *b.Approvals && *a.Favourites == *b.Favourites {
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

// CheckBallot checks a ballot against the event and its candidates: the
// approved candidates (in increasing id order, each once, so one ballot has
// one text) and the favourite (0: none, else one of the approved).
func CheckBallot(e *Event, candidates []int64, approved []int64, favourite int64, now time.Time) error {
	if !IsOpen(e, now) {
		return ErrClosed
	}
	if e.MaxChoices > 0 && len(approved) > e.MaxChoices {
		return fmt.Errorf("%w: at most %d approvals", ErrBallot, e.MaxChoices)
	}
	isCandidate := make(map[int64]bool, len(candidates))
	for _, id := range candidates {
		isCandidate[id] = true
	}
	for i, id := range approved {
		if !isCandidate[id] {
			return fmt.Errorf("%w: person %d is not a candidate", ErrBallot, id)
		}
		if i > 0 && approved[i-1] >= id {
			return fmt.Errorf("%w: the approvals must be in increasing order, each once", ErrBallot)
		}
	}
	if favourite != 0 && !slices.Contains(approved, favourite) {
		return fmt.Errorf("%w: the favourite must be approved", ErrBallot)
	}
	return nil
}
