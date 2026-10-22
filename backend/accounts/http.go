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
		httpx.WriteError(w, http.StatusBadRequest, "this method verifies an account: sign in first")
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

// Code of a verification refused because the account holds another document.
const codeOtherDocument = "other_document"

// Codes of a verification that is not done.
const (
	// codeHeldElsewhere: the document is another account's; the answer
	// carries the move_token that confirms its move here.
	codeHeldElsewhere = "document_elsewhere"
	codeNoMove        = "no_move"
)

// MoveTTL is how long the user has to confirm a move.
const MoveTTL = 10 * time.Minute

// handleVerify attaches a verified identity to the signed-in account
// (POST /api/me/verify/{provider}): the document of a verification provider,
// which gives the right to vote. A document held by another account does
// not move at once: the answer is 409 document_elsewhere with a move_token,
// and the move happens when the user confirms it (handleConfirmMove).
func (s *Service) handleVerify(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	p, ok := s.providers.Get(chi.URLParam(r, "provider"))
	if !ok {
		httpx.WriteError(w, http.StatusNotFound, "unknown verification method")
		return
	}
	if !providers.Verifies(p) {
		httpx.WriteError(w, http.StatusBadRequest, "this method opens an account, it does not verify one")
		return
	}
	count := func(outcome string) { metrics.VerificationsTotal.WithLabelValues(p.Name(), outcome).Inc() }
	body, ok := s.readProof(w, r, p)
	if !ok {
		count("refused")
		return
	}
	id, err := p.Verify(ctx, body)
	if err != nil {
		if errors.Is(err, providers.ErrInvalidProof) {
			count("refused")
			log.Warn().Err(err).Str("provider", p.Name()).Msg("verification refused")
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		count("error")
		httpx.InternalError(w, err, "verification failed")
		return
	}
	user := UserFrom(r)
	subject := p.Name() + ":" + id.Subject
	_, err = s.store.AttachVerification(ctx, user.ID, subject, false)
	switch {
	case errors.Is(err, ErrHeldElsewhere):
		token, hash, err := providers.NewSessionToken()
		if err == nil {
			err = s.store.CreateMove(ctx, user.ID, subject, hash, time.Now().Add(MoveTTL))
		}
		if err != nil {
			count("error")
			httpx.InternalError(w, err, "failed to keep the move")
			return
		}
		count(codeHeldElsewhere)
		httpx.WriteJSON(w, http.StatusConflict, map[string]string{
			"error": "this document is another account's: confirm to move it here", "code": codeHeldElsewhere, "move_token": token})
		return
	case errors.Is(err, ErrOtherDocument):
		count(codeOtherDocument)
		httpx.WriteJSON(w, http.StatusConflict, httpx.CodedError{Error: "this account is already verified with another document", Code: codeOtherDocument})
		return
	case err != nil:
		count("error")
		httpx.InternalError(w, err, "failed to attach verification")
		return
	}
	outcome := "ok"
	if user.Verified {
		outcome = "again"
	}
	count(outcome)
	log.Info().Str("provider", p.Name()).Int64("user_id", user.ID).Str("outcome", outcome).Msg("account verified")
	verified := *user
	verified.Verified = true
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": verified, "moved": false})
}

// handleConfirmMove moves to the signed-in account the document its last
// verification found on another account (POST /api/me/verify/move
// {"move_token"}): the votes of the other account come with it, and it
// loses the right to vote.
func (s *Service) handleConfirmMove(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req struct {
		MoveToken string `json:"move_token"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil || req.MoveToken == "" {
		httpx.WriteError(w, http.StatusBadRequest, "move_token required")
		return
	}
	user := UserFrom(r)
	subject, ok, err := s.store.ConsumeMove(ctx, user.ID, providers.HashToken(req.MoveToken))
	if err != nil {
		httpx.InternalError(w, err, "failed to read the move")
		return
	}
	if !ok {
		httpx.WriteJSON(w, http.StatusBadRequest, httpx.CodedError{Error: "the move expired or was used: verify again", Code: codeNoMove})
		return
	}
	provider, _, _ := strings.Cut(subject, ":")
	from, err := s.store.AttachVerification(ctx, user.ID, subject, true)
	switch {
	case errors.Is(err, ErrOtherDocument):
		metrics.VerificationsTotal.WithLabelValues(provider, codeOtherDocument).Inc()
		httpx.WriteJSON(w, http.StatusConflict, httpx.CodedError{Error: "this account is already verified with another document", Code: codeOtherDocument})
		return
	case err != nil:
		metrics.VerificationsTotal.WithLabelValues(provider, "error").Inc()
		httpx.InternalError(w, err, "failed to move the verification")
		return
	}
	if from != 0 {
		s.changed()
	}
	metrics.VerificationsTotal.WithLabelValues(provider, "moved").Inc()
	log.Info().Str("provider", provider).Int64("user_id", user.ID).Int64("from_user_id", from).Msg("verified identity moved to another account")
	verified := *user
	verified.Verified = true
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"user": verified, "moved": from != 0})
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

// handleDeleteAccount erases the signed-in user: their votes, sessions,
// consent and verified identity. Signing in again starts a new, empty
// account, which the same document verifies again.
func (s *Service) handleDeleteAccount(w http.ResponseWriter, r *http.Request) {
	user := UserFrom(r)
	if err := s.store.DeleteUser(r.Context(), user.ID); err != nil {
		httpx.InternalError(w, err, "failed to delete account")
		return
	}
	s.changed()
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
