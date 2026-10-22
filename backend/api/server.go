// Package api assembles the HTTP API: the Chi router, its middleware stack
// (security headers, CORS, rate limits, metrics) and /health, with the three
// parts of the site mounted under /api: mirror (the politicians' record),
// accounts (the users) and polls (the votes). Each part owns its routes and
// its tables. Serve wires them with the long-lived background workers
// (metrics endpoint, balance poller, archiver) that only the web service
// runs.
package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/mirror"
	"digital-mirror/backend/polls"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	"github.com/rs/zerolog/log"
)

// Pinger checks the database for /health.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Server holds the HTTP router: the three parts of the site mounted side by
// side under /api, each with its own routes and storage. mirror is the
// record of the politicians, accounts the users, polls the votes.
type Server struct {
	db       Pinger
	mirror   *mirror.Service
	accounts *accounts.Service
	polls    *polls.Service
	router   *chi.Mux
	// keyIP keys the rate limits by client address.
	keyIP func(r *http.Request) (string, error)
}

// NewServer builds the router with its middleware stack and mounts the
// parts' routes.
func NewServer(db Pinger, acc *accounts.Service, m *mirror.Service, p *polls.Service) *Server {
	r := chi.NewRouter()

	// Rate limit from env (default: 600 req/min per client address). A page
	// view makes about ten requests, and a mobile carrier puts many users
	// behind one address: the limit stops floods, not readers.
	rateLimit := envLimit("RATE_LIMIT_PER_MINUTE", 600)
	keyIP := keyByClientIP(clientIPHeader())

	// Middleware — applied in order:
	// securityHeaders → Logger → Recoverer → Timeout → CORS → RateLimit → metrics
	r.Use(securityHeaders)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(60 * time.Second))
	r.Use(cors.Handler(cors.Options{
		AllowOriginFunc:  corsOriginFunc(),
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Accept", "Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: true,
		MaxAge:           300,
	}))
	// After CORS, so a refused request still carries its CORS headers: the
	// page then sees a 429, not an opaque network error.
	r.Use(httprate.Limit(rateLimit, 1*time.Minute, httprate.WithKeyFuncs(keyIP)))
	r.Use(metrics.Middleware)

	server := &Server{db: db, mirror: m, accounts: acc, polls: p, router: r, keyIP: keyIP}
	server.setupRoutes()
	return server
}

// Handler returns the root HTTP handler (for tests and embedding).
func (s *Server) Handler() http.Handler {
	return s.router
}

// Routes returns every registered "METHOD /path" pair, in walk order.
func (s *Server) Routes() ([]string, error) {
	var routes []string
	err := chi.Walk(s.router, func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		routes = append(routes, method+" "+route)
		return nil
	})
	return routes, err
}

// Start serves the API on addr and blocks until the listener fails.
func (s *Server) Start(addr string) error {
	log.Info().Str("addr", addr).Msg("Starting web server")
	return http.ListenAndServe(addr, s.router)
}

// corsOriginFunc returns a CORS origin checker based on the ALLOWED_ORIGINS env var.
// If ALLOWED_ORIGINS is set (comma-separated), only those origins are permitted.
// Otherwise, localhost/127.0.0.1 origins are allowed for development.
func corsOriginFunc() func(r *http.Request, origin string) bool {
	if env := os.Getenv("ALLOWED_ORIGINS"); env != "" {
		allowed := make(map[string]struct{})
		for _, o := range strings.Split(env, ",") {
			o = strings.TrimSpace(o)
			if o != "" {
				allowed[o] = struct{}{}
			}
		}
		return func(r *http.Request, origin string) bool {
			_, ok := allowed[origin]
			return ok
		}
	}
	return func(r *http.Request, origin string) bool {
		return strings.HasPrefix(origin, "http://localhost:") ||
			strings.HasPrefix(origin, "http://127.0.0.1:") ||
			strings.HasPrefix(origin, "https://localhost:") ||
			strings.HasPrefix(origin, "https://127.0.0.1:")
	}
}

// securityHeaders adds standard security headers to all responses.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src https://fonts.gstatic.com; img-src 'self' data: https:; connect-src 'self'")

		// HSTS only when behind HTTPS (reverse proxy sets X-Forwarded-Proto)
		if r.Header.Get("X-Forwarded-Proto") == "https" {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}

		next.ServeHTTP(w, r)
	})
}

// setupRoutes mounts /health and the parts' routes under /api.
func (s *Server) setupRoutes() {
	// Health check endpoint (kept on main API for external LB probes)
	s.router.Get("/health", s.handleHealth)
	s.router.Route("/api", func(r chi.Router) {
		s.mirror.Routes(r)
		// A proof costs a check: a tighter limit, shared by the challenges,
		// the sign-ins and the verifications.
		s.accounts.Routes(r, accounts.SignInLimit(envLimit("SIGNIN_RATE_LIMIT_PER_MINUTE", 30), s.keyIP))
		s.polls.Routes(r)
	})
}

// handleHealth checks database connectivity and returns server health status.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if err := s.db.Ping(r.Context()); err != nil {
		log.Error().Err(err).Msg("health check: db ping failed")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"status": "error"})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
