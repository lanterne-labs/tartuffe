package vote

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"digital-mirror/backend/httpx"
)

// Routes serves what auditors read of tartuffe-vote/2 beside the log
// itself (/api/vote-log, served by the polls): the checkpoints with their
// signed notes. Voters' browsers never call these: they read the mirror.
func Routes(store *Store) func(r chi.Router) {
	return func(r chi.Router) {
		r.Get("/vote2/checkpoints", func(w http.ResponseWriter, req *http.Request) {
			cps, err := store.AllCheckpoints(req.Context())
			if err != nil {
				httpx.InternalError(w, err, "failed to read the checkpoints")
				return
			}
			if cps == nil {
				cps = []Checkpoint{}
			}
			httpx.WriteJSON(w, http.StatusOK, cps)
		})
		r.Get("/vote2/checkpoints/{idx}", func(w http.ResponseWriter, req *http.Request) {
			idx, _ := strconv.ParseInt(chi.URLParam(req, "idx"), 10, 64)
			cps, err := store.AllCheckpoints(req.Context())
			if err != nil {
				httpx.InternalError(w, err, "failed to read the checkpoints")
				return
			}
			for _, cp := range cps {
				if cp.Index == idx {
					w.Header().Set("Content-Type", "text/plain; charset=utf-8")
					_, _ = w.Write([]byte(cp.Note))
					return
				}
			}
			http.NotFound(w, req)
		})
	}
}
