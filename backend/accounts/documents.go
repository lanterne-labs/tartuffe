package accounts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"time"

	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/httpx"
	"digital-mirror/backend/metrics"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"
)

// ProveDocument checks the proof of a document (a passport through
// ZKPassport, or the test provider) of the {provider} path parameter, after
// consuming its one-time challenge, for a part of the site that needs a
// document but no account: the anonymous ballots register a voting key with
// it. It answers the refusal itself and returns false then. Document is the
// identifier to keep: an HMAC under the subject key, never the provider's.
func (s *Service) ProveDocument(w http.ResponseWriter, r *http.Request) (document, provider string, body []byte, ok bool) {
	p, found := s.providers.Get(chi.URLParam(r, "provider"))
	if !found || !providers.Verifies(p) {
		httpx.WriteError(w, http.StatusNotFound, "unknown verification method")
		return "", "", nil, false
	}
	if len(s.SubjectKey) < 32 {
		log.Error().Msg("AUTH_SUBJECT_KEY is not set: documents cannot be registered")
		httpx.WriteError(w, http.StatusServiceUnavailable, "document checks are not configured")
		return "", "", nil, false
	}
	count := func(outcome string) { metrics.VerificationsTotal.WithLabelValues(p.Name(), outcome).Inc() }
	body, ok = s.readProof(w, r, p)
	if !ok {
		count("refused")
		return "", "", nil, false
	}
	id, err := p.Verify(r.Context(), body)
	if err != nil {
		if errors.Is(err, providers.ErrInvalidProof) {
			count("refused")
			log.Warn().Err(err).Str("provider", p.Name()).Msg("document proof refused")
			httpx.WriteError(w, http.StatusBadRequest, err.Error())
			return "", "", nil, false
		}
		count("error")
		httpx.InternalError(w, err, "document check failed")
		return "", "", nil, false
	}
	count("ok")
	mac := hmac.New(sha256.New, s.SubjectKey)
	mac.Write([]byte(p.Name() + ":" + id.Subject))
	return hex.EncodeToString(mac.Sum(nil)), p.Name(), body, true
}

// handleNewSession opens another session of the signed-in user, for a
// device they link (the token travels end-to-end encrypted with their voting
// key, the server sees only that the account opened a session).
func (s *Service) handleNewSession(w http.ResponseWriter, r *http.Request) {
	user := UserFrom(r)
	token, hash, err := providers.NewSessionToken()
	if err != nil {
		httpx.InternalError(w, err, "failed to create session token")
		return
	}
	expires := time.Now().Add(providers.SessionTTL)
	if err := s.store.CreateSession(r.Context(), user.ID, hash, expires); err != nil {
		httpx.InternalError(w, err, "failed to create session")
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"token": token, "expires_at": expires})
}
