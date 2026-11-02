// Package accounts is the site's accounts: who is signed in, and with which
// provider. It serves /api/auth/*, and gives the other parts of the site
// RequireUser, UserFrom and ViewerID; they never read its tables. It attaches
// no document to an account: ProveDocument checks the proof of a document
// for the polls (which admit a voting key with it) and keeps nothing of it,
// so nothing ties an account to a document.
package accounts

import (
	"net/http"
	"time"

	"digital-mirror/backend/accounts/providers"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/httprate"
	"github.com/rs/zerolog/log"
)

// Service serves the accounts.
type Service struct {
	store     Store
	providers *providers.Registry
	// SubjectKey keys the HMAC of the document identifiers ProveDocument
	// gives (AUTH_SUBJECT_KEY).
	SubjectKey []byte
}

// NewService serves the accounts of the store, signed in with the enabled
// providers (none: nobody can sign in).
func NewService(store Store, reg *providers.Registry) *Service {
	return &Service{store: store, providers: reg}
}

// Routes mounts the accounts' endpoints on the /api router. signin limits the
// requests that cost a proof check (challenges and sign-ins).
func (s *Service) Routes(r chi.Router, signin func(http.Handler) http.Handler) {
	r.Get("/auth/providers", s.handleAuthProviders)
	r.Group(func(r chi.Router) {
		r.Use(signin)
		r.Post("/auth/challenge/{provider}", s.handleAuthChallenge)
		r.Post("/auth/signin/{provider}", s.handleAuthSignIn)
	})
	r.Get("/auth/me", s.RequireUser(s.handleAuthMe))
	r.Patch("/auth/me", s.RequireUser(s.handleSetDisplayName))
	r.Delete("/auth/me", s.RequireUser(s.handleDeleteAccount))
	r.Post("/auth/signout", s.RequireUser(s.handleAuthSignOut))
	r.Post("/auth/sessions", s.RequireUser(s.handleNewSession))
}

// SignInLimit is the default limit of the proof checks: 30 per minute and
// client address (a sign-in is two requests, and users behind one carrier
// address share it).
func SignInLimit(limit int, key httprate.KeyFunc) func(http.Handler) http.Handler {
	return httprate.Limit(limit, time.Minute, httprate.WithKeyFuncs(key))
}

// ViewerID is the signed-in user of a public request, 0 when signed out or
// when the session cannot be read (the page then shows nothing as theirs).
func (s *Service) ViewerID(r *http.Request) int64 {
	token := bearerToken(r)
	if token == "" {
		return 0
	}
	user, err := s.store.SessionUser(r.Context(), providers.HashToken(token))
	if err != nil {
		log.Warn().Err(err).Msg("failed to read the viewer's session")
		return 0
	}
	if user == nil {
		return 0
	}
	if _, ok := s.providers.Get(user.Provider); !ok {
		return 0
	}
	return user.ID
}

// Providers are the enabled providers.
func (s *Service) Providers() *providers.Registry { return s.providers }
