package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog/log"
)

// accessLog logs each request without who made it: the method, the path
// (without its query, which may carry a search), the status, the size and
// the duration. The client's address is never written (privacy: the rate
// limits keep it in memory for a minute, nothing keeps it longer).
func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		defer func() {
			log.Info().Str("method", r.Method).Str("path", r.URL.Path).Int("status", ww.Status()).
				Int("bytes", ww.BytesWritten()).Dur("took", time.Since(start)).Msg("request")
		}()
		next.ServeHTTP(ww, r)
	})
}
