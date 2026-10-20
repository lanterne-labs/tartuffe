package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"digital-mirror/backend/poll"
	"digital-mirror/backend/storage"

	"github.com/go-chi/chi/v5"
)

// handleListPolls lists the events, the featured one first.
func (s *Server) handleListPolls(w http.ResponseWriter, r *http.Request) {
	events, err := s.storer.ListPollEvents(r.Context())
	if err != nil {
		httpInternalError(w, err, "failed to list polls")
		return
	}
	writeJSON(w, http.StatusOK, events)
}

// pollEvent loads the event of the {slug} path parameter, answering 404 itself.
func (s *Server) pollEvent(w http.ResponseWriter, r *http.Request) (*storage.PollEvent, bool) {
	e, err := s.storer.GetPollEvent(r.Context(), chi.URLParam(r, "slug"))
	if err != nil {
		httpInternalError(w, err, "failed to get poll")
		return nil, false
	}
	if e == nil {
		writeError(w, http.StatusNotFound, "no such poll")
		return nil, false
	}
	return e, true
}

// handleGetPoll returns the event and its results.
func (s *Server) handleGetPoll(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	tallies, voters, err := s.storer.GetPollTallies(r.Context(), e.ID)
	if err != nil {
		httpInternalError(w, err, "failed to get poll tallies")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"event":   e,
		"results": poll.Compute(e, tallies, voters, time.Now()),
	})
}

// ballotResponse is the signed-in user's ballot.
type ballotResponse struct {
	PersonIDs []int64 `json:"person_ids"`
	Consented bool    `json:"consented"`
}

func (s *Server) handleGetBallot(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	user := userFrom(r)
	ids, err := s.storer.GetPollBallot(r.Context(), e.ID, user.ID)
	if err != nil {
		httpInternalError(w, err, "failed to get ballot")
		return
	}
	writeJSON(w, http.StatusOK, ballotResponse{PersonIDs: ids, Consented: user.ConsentAt != nil})
}

// handleSaveBallot replaces the user's ballot: {"person_ids": [first, ...],
// "consent": true}. The first ballot needs the consent to store a political
// opinion; an empty list withdraws the ballot.
func (s *Server) handleSaveBallot(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	user := userFrom(r)
	var req struct {
		PersonIDs []int64 `json:"person_ids"`
		Consent   bool    `json:"consent"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid ballot")
		return
	}
	if user.ConsentAt == nil && !req.Consent {
		writeError(w, http.StatusBadRequest, "consent required")
		return
	}
	candidates, _, err := s.storer.GetPollTallies(ctx, e.ID)
	if err != nil {
		httpInternalError(w, err, "failed to get poll candidates")
		return
	}
	ids := make([]int64, len(candidates))
	for i, c := range candidates {
		ids[i] = c.PersonID
	}
	if err := poll.CheckBallot(e, ids, req.PersonIDs, time.Now()); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, poll.ErrClosed) {
			status = http.StatusForbidden
		}
		writeError(w, status, err.Error())
		return
	}
	if err := s.storer.SavePollBallot(ctx, e.ID, user.ID, req.PersonIDs); err != nil {
		httpInternalError(w, err, "failed to save ballot")
		return
	}
	if req.PersonIDs == nil {
		req.PersonIDs = []int64{}
	}
	writeJSON(w, http.StatusOK, ballotResponse{PersonIDs: req.PersonIDs, Consented: true})
}
