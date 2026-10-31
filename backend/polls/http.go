package polls

import (
	"net/http"
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

// handleGetPoll returns the event and its candidates. The results are
// the vote ledger's (isoloir), on its mirror.
func (s *Service) handleGetPoll(w http.ResponseWriter, r *http.Request) {
	e, ok := s.pollEvent(w, r)
	if !ok {
		return
	}
	options, err := s.Options(r.Context(), e)
	if err != nil {
		httpx.InternalError(w, err, "failed to get poll candidates")
		return
	}
	now := time.Now()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"event":    e,
		"results":  map[string]any{"options": options},
		"open":     IsOpen(e, now),
		"blackout": InBlackout(e, now),
	})
}
