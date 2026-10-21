package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"digital-mirror/backend/auth"
	"digital-mirror/backend/storage"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// maxAuthBody bounds a sign-in payload (a zero-knowledge proof is the largest).
const maxAuthBody = 4 << 20

type ctxKey int

const userKey ctxKey = 0

// writeJSON writes v as the JSON body with the status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Error().Err(err).Msg("failed to encode JSON response")
	}
}

// writeError answers a client error with {"error": msg}.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if t, ok := strings.CutPrefix(h, "Bearer "); ok {
		return strings.TrimSpace(t)
	}
	return ""
}

// requireUser serves next only for a live session of an enabled provider:
// turning a provider off (AUTH_PROVIDERS) ends its sessions at once.
func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := bearerToken(r)
		if token == "" {
			writeError(w, http.StatusUnauthorized, "sign in first")
			return
		}
		user, err := s.storer.GetSessionUser(r.Context(), auth.HashToken(token))
		if err != nil {
			httpInternalError(w, err, "failed to read session")
			return
		}
		if user == nil {
			writeError(w, http.StatusUnauthorized, "session expired")
			return
		}
		if _, ok := s.providers.Get(user.Provider); !ok {
			writeError(w, http.StatusUnauthorized, "this sign-in method is no longer enabled")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, user)))
	}
}

func userFrom(r *http.Request) *storage.AppUser {
	u, _ := r.Context().Value(userKey).(*storage.AppUser)
	return u
}

// handleAuthProviders lists the enabled sign-in methods.
func (s *Server) handleAuthProviders(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.providers.Infos())
}

// handleAuthChallenge issues a one-time challenge for a provider whose proofs
// must carry one (auth.Challenger).
func (s *Server) handleAuthChallenge(w http.ResponseWriter, r *http.Request) {
	p, ok := s.providers.Get(chi.URLParam(r, "provider"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown sign-in method")
		return
	}
	c, ok := p.(auth.Challenger)
	if !ok {
		writeError(w, http.StatusBadRequest, "this sign-in method takes no challenge")
		return
	}
	challenge, _, err := auth.NewSessionToken()
	if err != nil {
		httpInternalError(w, err, "failed to create challenge")
		return
	}
	expires := time.Now().Add(c.ChallengeTTL())
	if err := s.storer.CreateAuthChallenge(r.Context(), p.Name(), challenge, expires); err != nil {
		httpInternalError(w, err, "failed to store challenge")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"challenge": challenge, "expires_at": expires})
}

// handleAuthSignIn checks the provider's payload and opens a session.
func (s *Server) handleAuthSignIn(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.providers.Get(chi.URLParam(r, "provider"))
	if !ok {
		writeError(w, http.StatusNotFound, "unknown sign-in method")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxAuthBody))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
		return
	}
	if _, ok := p.(auth.Challenger); ok {
		var c struct {
			Challenge string `json:"challenge"`
		}
		if err := json.Unmarshal(body, &c); err != nil || c.Challenge == "" {
			writeError(w, http.StatusBadRequest, "challenge required")
			return
		}
		live, err := s.storer.ConsumeAuthChallenge(ctx, p.Name(), c.Challenge)
		if err != nil {
			httpInternalError(w, err, "failed to check challenge")
			return
		}
		if !live {
			writeError(w, http.StatusBadRequest, "challenge expired or already used: start again")
			return
		}
	}
	id, err := p.Verify(ctx, body)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidProof) {
			log.Warn().Err(err).Str("provider", p.Name()).Msg("sign-in refused")
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		httpInternalError(w, err, "sign-in verification failed")
		return
	}
	user, err := s.storer.UpsertAppUser(ctx, id.Provider, id.Subject, id.DisplayName)
	if err != nil {
		httpInternalError(w, err, "failed to save user")
		return
	}
	token, hash, err := auth.NewSessionToken()
	if err != nil {
		httpInternalError(w, err, "failed to create session token")
		return
	}
	expires := time.Now().Add(auth.SessionTTL)
	if err := s.storer.CreateUserSession(ctx, user.ID, hash, expires); err != nil {
		httpInternalError(w, err, "failed to create session")
		return
	}
	log.Info().Str("provider", p.Name()).Int64("user_id", user.ID).Msg("user signed in")
	writeJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": expires, "user": user})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, userFrom(r))
}

// handleDeleteAccount erases the signed-in user: their votes, sessions and
// consent. Signing in again with the same document starts a new, empty account.
func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r)
	if err := s.storer.DeleteAppUser(r.Context(), user.ID); err != nil {
		httpInternalError(w, err, "failed to delete account")
		return
	}
	log.Info().Int64("user_id", user.ID).Str("provider", user.Provider).Msg("user deleted their account")
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAuthSignOut(w http.ResponseWriter, r *http.Request) {
	if err := s.storer.DeleteUserSession(r.Context(), auth.HashToken(bearerToken(r))); err != nil {
		httpInternalError(w, err, "failed to end session")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
