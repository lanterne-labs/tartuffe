package mirror

import (
	"encoding/json"
	"net/http"
	"time"

	"digital-mirror/backend/httpx"
	"digital-mirror/backend/storage"
)

// podiumResponse is a podium: the live one, or the one a past week ended on.
type podiumResponse struct {
	// Week is the Monday (UTC) that starts the podium's week, YYYY-MM-DD.
	Week string `json:"week"`
	// Live: the podium of the current week, as it stands now.
	Live    bool                  `json:"live"`
	Persons []storage.PodiumEntry `json:"persons"`
	// Weeks are the past weeks that have a podium, the latest first.
	Weeks []string `json:"weeks"`
}

// handlePodium returns the podium of ?week=YYYY-MM-DD (any day of the week),
// the live one without it or for the current week.
func (s *Service) handlePodium(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	current := storage.WeekStart(time.Now())
	week := current
	if v := r.URL.Query().Get("week"); v != "" {
		day, err := time.Parse(time.DateOnly, v)
		if err != nil {
			http.Error(w, "week must be a date, YYYY-MM-DD", http.StatusBadRequest)
			return
		}
		if week = storage.WeekStart(day); week.After(current) {
			week = current
		}
	}
	live := week.Equal(current)

	var persons []storage.PodiumEntry
	var err error
	if live {
		persons, err = s.store.LivePodium(ctx)
	} else {
		persons, err = s.store.PodiumSnapshot(ctx, week)
	}
	if err != nil {
		httpx.InternalError(w, err, "failed to get the podium")
		return
	}
	past, err := s.store.PodiumWeeks(ctx, current)
	if err != nil {
		httpx.InternalError(w, err, "failed to get the podium weeks")
		return
	}
	res := podiumResponse{Week: week.Format(time.DateOnly), Live: live, Persons: persons, Weeks: make([]string, 0, len(past))}
	for _, p := range past {
		res.Weeks = append(res.Weeks, p.Format(time.DateOnly))
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60, stale-while-revalidate=300")
	json.NewEncoder(w).Encode(res)
}
