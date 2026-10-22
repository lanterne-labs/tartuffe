package polls

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/httpx"
	"digital-mirror/backend/metrics"

	"github.com/go-chi/chi/v5"
)

// handleListPolls lists the events, the featured one first.
func (s *Service) handleListPolls(w http.ResponseWriter, r *http.Request) {
	events, err := s.store.ListEvents(r.Context())
	if err != nil {
		httpx.InternalError(w, err, "failed to list polls")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, events)
}

// pollEvent loads the event of the {slug} path parameter, answering 404 itself.
func (s *Service) pollEvent(w http.ResponseWriter, r *http.Request) (*Event, bool) {
	e, err := s.store.GetEvent(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		httpx.InternalError(w, err, "failed to get poll")
		return nil, false
	}
	if e == nil {
		httpx.WriteError(w, http.StatusNotFound, "no such poll")
		return nil, false
	}
	return e, true
}

// handleGetPoll returns the event and its results.
func (s *Service) handleGetPoll(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	tallies, voters, err := s.results.get(r.Context(), e.ID, s.tallies)
	if err != nil {
		httpx.InternalError(w, err, "failed to get poll tallies")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"event":   e,
		"results": Compute(e, tallies, voters, time.Now()),
	})
}

// ballotResponse is the signed-in user's ballot: PersonIDs are their votes,
// first choice first, out of MaxChoices.
type ballotResponse struct {
	PersonIDs  []int64 `json:"person_ids"`
	Consented  bool    `json:"consented"`
	MaxChoices int     `json:"max_choices"`
	Open       bool    `json:"open"`
}

func newBallotResponse(e *Event, ids []int64, consented bool) ballotResponse {
	return ballotResponse{PersonIDs: ids, Consented: consented, MaxChoices: e.MaxChoices, Open: IsOpen(e, time.Now())}
}

// ballotErrorResponse refuses a ballot change; Ballot is the ballot as
// stored when the refusal concerns it (ballot_full, not_on_ballot), so the
// page can show what to remove.
type ballotErrorResponse struct {
	Error  string          `json:"error"`
	Code   string          `json:"code"`
	Ballot *ballotResponse `json:"ballot,omitempty"`
}

// Codes of a refused ballot change.
const (
	codeClosed          = "closed"
	codeBallotFull      = "ballot_full"
	codeNotOnBallot     = "not_on_ballot"
	codeConsentRequired = "consent_required"
	codeInvalid         = "invalid_ballot"
	// codeVerificationRequired: an account without a verified identity
	// cannot vote.
	codeVerificationRequired = "verification_required"
)

var errConsentRequired = errors.New("consent required: a ballot is a political opinion, it is stored only with your consent")

func (s *Service) handleGetBallot(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	user := accounts.UserFrom(r)
	ids, err := s.store.Ballot(r.Context(), e.ID, user.ID)
	if err != nil {
		httpx.InternalError(w, err, "failed to get ballot")
		return
	}
	consented, err := s.store.Consented(r.Context(), user.ID)
	if err != nil {
		httpx.InternalError(w, err, "failed to get consent")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, newBallotResponse(e, ids, consented))
}

// changeBallot applies edit to the user's ballot of the event and answers
// with the ballot as stored, or with the reason of the refusal. A ballot that
// gains a vote needs the user's consent (given before, or now with consent);
// one that only loses votes does not.
func (s *Service) changeBallot(w http.ResponseWriter, r *http.Request, e *Event, consent bool, edit func(current []int64) ([]int64, error)) {
	ctx := r.Context()
	user := accounts.UserFrom(r)
	if !IsOpen(e, time.Now()) {
		httpx.WriteJSON(w, http.StatusForbidden, ballotErrorResponse{Error: ErrClosed.Error(), Code: codeClosed})
		return
	}
	candidates, err := s.store.CandidateIDs(ctx, e.ID)
	if err != nil {
		httpx.InternalError(w, err, "failed to get poll candidates")
		return
	}
	consentedBefore, err := s.store.Consented(ctx, user.ID)
	if err != nil {
		httpx.InternalError(w, err, "failed to get consent")
		return
	}
	var stored []int64
	ids, err := s.store.UpdateBallot(ctx, e.ID, user.ID, consent, func(current []int64) ([]int64, error) {
		stored = current
		next, err := edit(current)
		if err != nil {
			return nil, err
		}
		limits := e
		if !gainsVote(current, next) {
			// Removals and reorders: allowed even on a ballot above a
			// max_choices lowered since it was cast.
			relaxed := *e
			relaxed.MaxChoices = max(e.MaxChoices, len(current))
			limits = &relaxed
		} else if !user.Verified {
			return nil, ErrNotVerified
		} else if !consentedBefore && !consent {
			return nil, errConsentRequired
		}
		return next, CheckBallot(limits, candidates, next, time.Now())
	})
	consented := consentedBefore || consent
	outcome := "ok"
	defer func() { metrics.BallotChangesTotal.WithLabelValues(outcome).Inc() }()
	switch {
	case err == nil:
		s.results.invalidate(e.ID)
		httpx.WriteJSON(w, http.StatusOK, newBallotResponse(e, ids, consented))
	case errors.Is(err, ErrFull), errors.Is(err, ErrNotOnBallot):
		b := newBallotResponse(e, stored, consentedBefore)
		code := codeBallotFull
		if errors.Is(err, ErrNotOnBallot) {
			code = codeNotOnBallot
		}
		outcome = code
		httpx.WriteJSON(w, http.StatusConflict, ballotErrorResponse{Error: err.Error(), Code: code, Ballot: &b})
	case errors.Is(err, ErrNotVerified):
		outcome = codeVerificationRequired
		httpx.WriteJSON(w, http.StatusForbidden, ballotErrorResponse{Error: err.Error(), Code: codeVerificationRequired})
	case errors.Is(err, errConsentRequired):
		outcome = codeConsentRequired
		httpx.WriteJSON(w, http.StatusBadRequest, ballotErrorResponse{Error: err.Error(), Code: codeConsentRequired})
	case errors.Is(err, ErrClosed):
		outcome = codeClosed
		httpx.WriteJSON(w, http.StatusForbidden, ballotErrorResponse{Error: err.Error(), Code: codeClosed})
	case errors.Is(err, ErrBallot), errors.Is(err, ErrNotCandidate):
		outcome = codeInvalid
		httpx.WriteJSON(w, http.StatusBadRequest, ballotErrorResponse{Error: err.Error(), Code: codeInvalid})
	default:
		outcome = "error"
		httpx.InternalError(w, err, "failed to save ballot")
	}
}

// gainsVote tells whether next votes for someone current does not.
func gainsVote(current, next []int64) bool {
	for _, id := range next {
		if !slices.Contains(current, id) {
			return true
		}
	}
	return false
}

// handleSaveBallot replaces the user's ballot: {"person_ids": [first, ...],
// "consent": true}. It serves to reorder the votes; an empty list withdraws
// them all.
func (s *Service) handleSaveBallot(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	var req struct {
		PersonIDs []int64 `json:"person_ids"`
		Consent   bool    `json:"consent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		httpx.WriteJSON(w, http.StatusBadRequest, ballotErrorResponse{Error: "invalid ballot", Code: codeInvalid})
		return
	}
	if req.PersonIDs == nil {
		req.PersonIDs = []int64{}
	}
	s.changeBallot(w, r, e, req.Consent, func([]int64) ([]int64, error) { return req.PersonIDs, nil })
}

// handleAddVote casts one vote: {"person_id": id, "consent": true}, after the
// votes already cast. With "replace": other_id the vote takes the place (and
// rank) of the vote for other_id. When every vote is used the answer is 409
// ballot_full with the ballot, so the page can ask which vote to give up.
func (s *Service) handleAddVote(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	var req struct {
		PersonID int64 `json:"person_id"`
		Replace  int64 `json:"replace"`
		Consent  bool  `json:"consent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.PersonID <= 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, ballotErrorResponse{Error: "person_id required", Code: codeInvalid})
		return
	}
	s.changeBallot(w, r, e, req.Consent, func(current []int64) ([]int64, error) {
		return AddVote(e, current, req.PersonID, req.Replace)
	})
}

// handleRemoveVote takes back the vote for {personID}; the votes below move
// up a rank. Removing a vote not cast changes nothing.
func (s *Service) handleRemoveVote(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	personID, err := strconv.ParseInt(chi.URLParam(r, "personID"), 10, 64)
	if err != nil || personID <= 0 {
		httpx.WriteJSON(w, http.StatusBadRequest, ballotErrorResponse{Error: "invalid person id", Code: codeInvalid})
		return
	}
	s.changeBallot(w, r, e, false, func(current []int64) ([]int64, error) {
		return RemoveVote(current, personID), nil
	})
}

// myEventVotes is one event on the profile page with the user's votes.
type myEventVotes struct {
	Event *Event       `json:"event"`
	Open  bool         `json:"open"`
	Votes []UserChoice `json:"votes"`
}

// handleMyVotes lists the user's votes per event: every open event (with no
// vote yet, it shows the votes left), and the closed ones they voted in.
func (s *Service) handleMyVotes(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	user := accounts.UserFrom(r)
	events, err := s.store.ListEvents(ctx)
	if err != nil {
		httpx.InternalError(w, err, "failed to list polls")
		return
	}
	choices, err := s.choices(ctx, user.ID)
	if err != nil {
		httpx.InternalError(w, err, "failed to get votes")
		return
	}
	byEvent := map[int64][]UserChoice{}
	for _, c := range choices {
		byEvent[c.EventID] = append(byEvent[c.EventID], c)
	}
	now := time.Now()
	out := []myEventVotes{}
	for i := range events {
		e := &events[i]
		votes, open := byEvent[e.ID], IsOpen(e, now)
		if !open && len(votes) == 0 {
			continue
		}
		if votes == nil {
			votes = []UserChoice{}
		}
		out = append(out, myEventVotes{Event: e, Open: open, Votes: votes})
	}
	consented, err := s.store.Consented(ctx, user.ID)
	if err != nil {
		httpx.InternalError(w, err, "failed to get consent")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": user, "consented": consented, "events": out})
}
