// Package api is the Digital Mirror HTTP API: the Chi router, its middleware
// stack, the JSON handlers and the SSE event stream. Serve wires the server
// together with the long-lived background workers (metrics endpoint, balance
// poller, pipeline watchdog, archiver) that only the web service runs.
package api

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"digital-mirror/backend/auth"
	"digital-mirror/backend/core"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/storage"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"
	"github.com/rs/zerolog/log"
)

// Storer is the storage surface the HTTP handlers use.
type Storer interface {
	Ping(ctx context.Context) error
	GetPersonsRankedByScore(ctx context.Context, limit int, offset int, entityType string, search string, sort string, minFacts int) ([]storage.PersonSummary, error)
	GetPersonByID(ctx context.Context, personID int64) (*storage.PersonSummary, error)
	GetPersonFacts(ctx context.Context, personID int64, limit int, offset int) (*storage.PaginatedFacts, error)
	GetPersonContradictions(ctx context.Context, personID int64, limit int, offset int) (*storage.PaginatedContradictions, error)
	GetPersonMembers(ctx context.Context, personID int64) ([]storage.MemberInfo, error)
	GetPersonHistory(ctx context.Context, personID int64) ([]storage.HistoryPoint, error)
	GetFactPersons(ctx context.Context, factID int64, limit int, offset int) ([]storage.PersonSummary, error)
	GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]core.StanceDocument, error)
	GetStanceContradictionCounts(ctx context.Context, personID int64) (map[int64]int, error)
	GetStanceGroupCounts(ctx context.Context, personID int64) (map[int64]int, error)
	GetStanceContradictionGroups(ctx context.Context, stanceID int64) ([]storage.ContradictionGroup, error)
	GetFactsInStance(ctx context.Context, stanceID int64) ([]storage.FactInStance, error)
	GetMaxEventID(ctx context.Context) (int64, error)
	GetEventsAfter(ctx context.Context, sinceID int64, limit int) ([]storage.Event, error)

	// Pre-polls and their signed-in users.
	ListPollEvents(ctx context.Context) ([]storage.PollEvent, error)
	GetPollEvent(ctx context.Context, slug string) (*storage.PollEvent, error)
	GetPollTallies(ctx context.Context, eventID int64) ([]storage.PollCandidateTally, int, error)
	GetPollBallot(ctx context.Context, eventID, userID int64) ([]int64, error)
	GetPollCandidateIDs(ctx context.Context, eventID int64) ([]int64, error)
	UpdatePollBallot(ctx context.Context, eventID, userID int64, consent bool, edit func(current []int64) ([]int64, error)) ([]int64, error)
	GetUserPollChoices(ctx context.Context, userID int64) ([]storage.UserPollChoice, error)
	DeleteAppUser(ctx context.Context, userID int64) error
	CreateUserReport(ctx context.Context, userID int64, kind string, targetID int64, reason string) (*storage.UserReport, error)
	GetUserReports(ctx context.Context, userID int64) ([]storage.UserReport, error)
	ListCorrections(ctx context.Context, beforeID int64, limit int) ([]storage.Correction, error)

	// The feed, the shared links and the score explanations.
	GetFeed(ctx context.Context, f storage.FeedFilter, after *storage.FeedCursor, limit, minFacts int) ([]storage.FeedItem, error)
	SetLike(ctx context.Context, userID int64, kind string, targetID int64, on bool) (int, error)
	GetShareTarget(ctx context.Context, kind string, id, personID int64) (*storage.ShareTarget, error)
	GetScoreExplanation(ctx context.Context, personID int64) (*storage.ScoreExplanation, error)
	UpsertAppUser(ctx context.Context, provider, subject, displayName string) (*storage.AppUser, error)
	CreateUserSession(ctx context.Context, userID int64, tokenHash []byte, expiresAt time.Time) error
	GetSessionUser(ctx context.Context, tokenHash []byte) (*storage.AppUser, error)
	DeleteUserSession(ctx context.Context, tokenHash []byte) error
	CreateAuthChallenge(ctx context.Context, provider, challenge string, expiresAt time.Time) error
	ConsumeAuthChallenge(ctx context.Context, provider, challenge string) (bool, error)
}

// Server holds the HTTP router and its dependencies.
type Server struct {
	storer    Storer
	providers *auth.Registry
	router    *chi.Mux
	results   *resultsCache
	// keyIP keys the rate limits by client address.
	keyIP func(r *http.Request) (string, error)
}

// NewServer builds the router with its middleware stack and routes. providers
// are the enabled sign-in methods (none: nobody can sign in to vote).
func NewServer(storer Storer, providers *auth.Registry) *Server {
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

	server := &Server{
		storer:    storer,
		providers: providers,
		router:    r,
		results:   newResultsCache(resultsTTL),
		keyIP:     keyIP,
	}

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

// httpInternalError logs err server-side and returns a generic 500 to the client,
// preventing internal details (e.g. DB connection strings) from leaking.
func httpInternalError(w http.ResponseWriter, err error, msg string) {
	log.Error().Err(err).Msg(msg)
	http.Error(w, "Internal server error", http.StatusInternalServerError)
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

// setupRoutes configures all the API routes
func (s *Server) setupRoutes() {
	// Health check endpoint (kept on main API for external LB probes)
	s.router.Get("/health", s.handleHealth)

	// API routes
	s.router.Route("/api", func(r chi.Router) {
		r.Get("/persons", s.handleGetPersons)
		r.Get("/person/{id}", s.handleGetPerson)
		r.Get("/person/{id}/facts", s.handleGetPersonFacts)
		r.Get("/person/{id}/contradictions", s.handleGetPersonContradictions)
		r.Get("/person/{id}/members", s.handleGetPersonMembers)
		r.Get("/person/{id}/history", s.handleGetPersonHistory)
		r.Get("/facts/{id}/persons", s.handleGetFactPersons)
		r.Get("/person/{id}/stances", s.handleGetPersonStances)
		r.Get("/stances/{id}/contradictions", s.handleGetStanceContradictions)
		r.Get("/stances/{id}/facts", s.handleGetStanceFacts)
		r.Get("/person/{id}/score", s.handleScoreExplanation)
		r.Get("/events", s.handleEventsSSE)
		r.Get("/feed", s.handleFeed)
		r.Get("/share/{kind}/{id}", s.handleShare)
		r.Get("/corrections", s.handleCorrections)
		r.Post("/reports", s.requireUser(s.handleCreateReport))
		r.Get("/me/reports", s.requireUser(s.handleMyReports))
		r.Put("/likes/{kind}/{id}", s.requireUser(s.handleLike))
		r.Delete("/likes/{kind}/{id}", s.requireUser(s.handleLike))

		r.Get("/auth/providers", s.handleAuthProviders)
		// A sign-in proof costs a verification: a tighter limit, shared
		// by the challenge and the sign-in (default 30 per minute: a sign-in is two
		// requests, and users behind one carrier address share it).
		r.Group(func(r chi.Router) {
			r.Use(httprate.Limit(envLimit("SIGNIN_RATE_LIMIT_PER_MINUTE", 30), time.Minute, httprate.WithKeyFuncs(s.keyIP)))
			r.Post("/auth/challenge/{provider}", s.handleAuthChallenge)
			r.Post("/auth/signin/{provider}", s.handleAuthSignIn)
		})
		r.Get("/auth/me", s.requireUser(s.handleAuthMe))
		r.Delete("/auth/me", s.requireUser(s.handleDeleteAccount))
		r.Get("/me/votes", s.requireUser(s.handleMyVotes))
		r.Post("/auth/signout", s.requireUser(s.handleAuthSignOut))

		r.Get("/polls", s.handleListPolls)
		r.Get("/polls/{slug}", s.handleGetPoll)
		r.Get("/polls/{slug}/ballot", s.requireUser(s.handleGetBallot))
		r.Post("/polls/{slug}/ballot", s.requireUser(s.handleSaveBallot))
		r.Post("/polls/{slug}/votes", s.requireUser(s.handleAddVote))
		r.Delete("/polls/{slug}/votes/{personID}", s.requireUser(s.handleRemoveVote))
	})
}
