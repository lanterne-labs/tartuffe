package polls

import (
	"net/http"
	"strconv"
	"time"

	"digital-mirror/backend/httpx"

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

// handleGetPoll returns the event and its results as the site shows them.
func (s *Service) handleGetPoll(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	var results any
	if s.ResultsOf != nil {
		var err error
		if results, err = s.ResultsOf(r.Context(), e); err != nil {
			httpx.InternalError(w, err, "failed to get poll results")
			return
		}
	}
	now := time.Now()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"event":    e,
		"results":  results,
		"open":     IsOpen(e, now),
		"blackout": InBlackout(e, now),
	})
}

// handleLog serves the public log's entries (anyone recounts them).
func (s *Service) handleLog(w http.ResponseWriter, r *http.Request) {
	after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := s.box.Log(r.Context(), after, limit)
	if err != nil {
		httpx.InternalError(w, err, "failed to read vote log")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, entries)
}
