package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/httpx"
	"digital-mirror/backend/metrics"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// maxAuthBody bounds a sign-in payload (a zero-knowledge proof is the largest).
const maxAuthBody = 4 << 20

type ctxKey int

const userKey ctxKey = 0

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// RequireUser serves next only for a live session of an enabled provider:
// turning a provider off (AUTH_PROVIDERS) ends its sessions at once.
func (s *Service) RequireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			httpx.WriteError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		user, err := s.store.SessionUser(r.Context(), providers.HashToken(token))
		if err != nil {
			httpx.InternalError(w, err, "failed to read session")
			return
		}
		if user == nil {
			httpx.WriteError(w, http.StatusUnauthorized, "session expired")
			return
		}
		if _, ok := s.providers.Get(user.Provider); !ok {
			httpx.WriteError(w, http.StatusUnauthorized, "this sign-in method is no longer enabled")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	}
}

// UserFrom is the signed-in user of a request served by RequireUser.
func UserFrom(r *http.Request) *User {
	u, _ := r.Context().Value(userKey).(*User)
	return u
}

// handleAuthProviders lists the enabled sign-in methods.
func (s *Service) handleAuthProviders(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, s.providers.Infos())
}

// handleAuthChallenge issues a one-time challenge for a provider whose proofs
// must carry one (providers.Challenger).
func (s *Service) handleAuthChallenge(w http.ResponseWriter, r *http.Request) {
	p, ok := s.providers.Get(chi.URLParam(r, "provider"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "unknown sign-in method")
		return
	}
	c, ok := p.(providers.Challenger)
	if !ok {
		httpx.WriteError(w, http.StatusBadRequest, "this sign-in method takes no challenge")
		return
	}
	challenge, _, err := providers.NewSessionToken()
	if err != nil {
		httpx.InternalError(w, err, "failed to create challenge")
		return
	}
	expires := time.Now().Add(c.ChallengeTTL())
	if err := s.store.CreateChallenge(r.Context(), p.Name(), challenge, expires); err != nil {
		httpx.InternalError(w, err, "failed to store challenge")
		return
	}
	metrics.AuthChallengesTotal.WithLabelValues(p.Name()).Inc()
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"challenge": challenge, "expires_at": expires})
}

// readProof reads the payload a provider checks, after consuming its
// one-time challenge (providers.Challenger). It answers the refusal itself and
// returns false then.
func (s *Service) readProof(w http.ResponseWriter, r *http.Request, p providers.Provider) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAuthBody))
	if err != nil {
		httpx.WriteError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return nil, false
	}
	if _, ok := p.(providers.Challenger); ok {
		var c struct {
			Challenge string `json:"challenge"`
		}
		if err := json.Unmarshal(body, &c); err != nil || c.Challenge == "" {
			httpx.WriteError(w, http.StatusBadRequest, "challenge required")
			return nil, false
		}
		live, err := s.store.ConsumeChallenge(r.Context(), p.Name(), c.Challenge)
		if err != nil {
			httpx.InternalError(w, err, "failed to check challenge")
			return nil, false
		}
		if !live {
			httpx.WriteError(w, http.StatusBadRequest, "challenge expired or already used: start again")
			return nil, false
		}
	}
	return body, true
}

// handleAuthSignIn checks the payload of an account provider and opens a
// session.
func (s *Service) handleAuthSignIn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.providers.Get(chi.URLParam(r, "provider"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "unknown sign-in method")
		return
	}
	if !providers.Opens(p) {
		httpx.WriteError(w, http.StatusBadRequest, "this method proves a document, it opens no account")
		return
	}
	body, ok := s.readProof(w, r, p)
	if !ok {
		metrics.AuthSignInsTotal.WithLabelValues(p.Name(), "refused").Inc()
		return
	}
	id, err := p.Verify(ctx, body)
	if err != nil {
		if errors.Is(err, providers.ErrInvalidProof) {
			metrics.AuthSignInsTotal.WithLabelValues(p.Name(), "refused").Inc()
			log.Warn().Err(err).Str("provider", p.Name()).Msg("sign-in refused")
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		metrics.AuthSignInsTotal.WithLabelValues(p.Name(), "error").Inc()
		httpx.InternalError(w, err, "sign-in verification failed")
		return
	}
	user, err := s.store.UpsertUser(ctx, id.Provider, id.Subject, id.DisplayName)
	if err != nil {
		metrics.AuthSignInsTotal.WithLabelValues(p.Name(), "error").Inc()
		httpx.InternalError(w, err, "failed to save user")
		return
	}
	token, hash, err := providers.NewSessionToken()
	if err != nil {
		metrics.AuthSignInsTotal.WithLabelValues(p.Name(), "error").Inc()
		httpx.InternalError(w, err, "failed to create session token")
		return
	}
	expires := time.Now().Add(providers.SessionTTL)
	if err := s.store.CreateSession(ctx, user.ID, hash, expires); err != nil {
		metrics.AuthSignInsTotal.WithLabelValues(p.Name(), "error").Inc()
		httpx.InternalError(w, err, "failed to create session")
		return
	}
	metrics.AuthSignInsTotal.WithLabelValues(p.Name(), "ok").Inc()
	log.Info().Str("provider", p.Name()).Int64("user_id", user.ID).Msg("user signed in")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": expires, "user": user})
}

// maxDisplayName bounds a pseudonym, in characters.
const maxDisplayName = 40

// handleSetDisplayName sets the pseudonym of the signed-in user (PATCH
// /api/auth/me {"display_name"}), 1 to 40 characters: the user alone sees
// it, no route shows it to anyone else.
func (s *Service) handleSetDisplayName(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DisplayName string `json:"display_name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, "display_name required")
		return
	}
	name := strings.Join(strings.Fields(req.DisplayName), " ")
	if n := utf8.RuneCountInString(name); n == 0 || n > maxDisplayName {
		httpx.WriteError(w, http.StatusBadRequest, "the pseudonym takes 1 to 40 characters")
		return
	}
	user := UserFrom(r)
	if err := s.store.SetDisplayName(r.Context(), user.ID, name); err != nil {
		httpx.InternalError(w, err, "failed to set the pseudonym")
		return
	}
	named := *user
	named.DisplayName = name
	httpx.WriteJSON(w, http.StatusOK, named)
}

func (s *Service) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSON(w, http.StatusOK, UserFrom(r))
}

// handleDeleteAccount erases the signed-in user and their sessions. Signing
// in again starts a new, empty account.
func (s *Service) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	user := UserFrom(r)
	if err := s.store.DeleteUser(r.Context(), user.ID); err != nil {
		httpx.InternalError(w, err, "failed to delete account")
		return
	}
	metrics.AccountsDeletedTotal.Inc()
	log.Info().Int64("user_id", user.ID).Str("provider", user.Provider).Msg("user deleted their account")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) handleAuthSignOut(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteSession(r.Context(), providers.HashToken(bearerToken(r))); err != nil {
		httpx.InternalError(w, err, "failed to end session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
