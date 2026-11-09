package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/accounts/accountstest"
	"digital-mirror/backend/accounts/providers"
	"digital-mirror/backend/mirror"
	"digital-mirror/backend/polls"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// pinger answers /health.
type pinger struct{ err error }

func (p pinger) Ping(context.Context) error { return p.err }

// onePoll is a polls store with one open event and no ballot, and no card:
// the server's tests check the assembly, not the parts.
type onePoll struct{}

func (onePoll) event() *polls.Event {
	return &polls.Event{ID: 1, Slug: "presidentielle-2027", MaxChoices: 3, MinVoters: 1, OpensAt: time.Now().Add(-time.Hour)}
}
func (p onePoll) ListEvents(context.Context) ([]polls.Event, error) {
	return []polls.Event{*p.event()}, nil
}
func (p onePoll) GetEvent(_ context.Context, slug string) (*polls.Event, error) {
	if slug != "presidentielle-2027" {
		return nil, nil
	}
	return p.event(), nil
}
func (onePoll) CandidateIDs(context.Context, int64) ([]int64, error) { return []int64{}, nil }
func (onePoll) Cards(context.Context, []int64) (map[int64]polls.Card, error) {
	return map[int64]polls.Card{}, nil
}

// newServer assembles the three parts on test stores (the mirror's is never
// reached by these tests), with noop sign-in.
func newServer(pingErr error) *Server {
	acc := accounts.NewService(accountstest.NewMemStore(), providers.NewRegistry(providers.Noop{}))
	return NewServer(pinger{pingErr}, acc, mirror.NewService(nil, acc), polls.NewService(onePoll{}, onePoll{}))
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRoutes(t *testing.T) {
	routes, err := newServer(nil).Routes()
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{
		"GET /health",
		"GET /api/persons",
		"GET /api/person/{id}",
		"GET /api/person/{id}/facts",
		"GET /api/person/{id}/contradictions",
		"GET /api/person/{id}/members",
		"GET /api/person/{id}/history",
		"GET /api/person/{id}/coverage",
		"GET /api/facts/{id}/persons",
		"GET /api/person/{id}/stances",
		"GET /api/stances/{id}/contradictions",
		"GET /api/stances/{id}/facts",
		"GET /api/events",
		"GET /api/auth/providers",
		"POST /api/auth/challenge/{provider}",
		"POST /api/auth/signin/{provider}",
		"PATCH /api/auth/me",
		"GET /api/auth/me",
		"DELETE /api/auth/me",
		"POST /api/auth/signout",
		"POST /api/auth/sessions",
		"GET /api/polls",
		"GET /api/polls/{slug}",
		"GET /api/person/{id}/score",
		"GET /api/feed",
		"GET /api/share/{kind}/{id}",
		"GET /api/corrections",
		"POST /api/reports",
		"GET /api/me/reports",
		"PUT /api/likes/{kind}/{id}",
		"DELETE /api/likes/{kind}/{id}",
	}, routes)
}

func TestHealth(t *testing.T) {
	rec := get(t, newServer(nil).Handler(), "/health")
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"status":"ok"}`, rec.Body.String())

	rec = get(t, newServer(errors.New("down")).Handler(), "/health")
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.JSONEq(t, `{"status":"error"}`, rec.Body.String())
}

func TestSecurityHeaders(t *testing.T) {
	rec := get(t, newServer(nil).Handler(), "/health")
	assert.Equal(t, "DENY", rec.Header().Get("X-Frame-Options"))
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
	assert.Equal(t, "strict-origin-when-cross-origin", rec.Header().Get("Referrer-Policy"))
	assert.NotEmpty(t, rec.Header().Get("Content-Security-Policy"))
	assert.Empty(t, rec.Header().Get("Strict-Transport-Security"), "HSTS only behind https")
}

// TestVotePreflight: the frontend is on another origin, so removing a vote
// (DELETE) or giving a +1 (PUT) needs the CORS preflight to allow the method.
func TestPreflight(t *testing.T) {
	h := newServer(nil).Handler()
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete} {
		req := httptest.NewRequest(http.MethodOptions, "/api/auth/me", nil)
		req.Header.Set("Origin", "http://localhost:3000")
		req.Header.Set("Access-Control-Request-Method", method)
		req.Header.Set("Access-Control-Request-Headers", "Authorization")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		assert.Equal(t, "http://localhost:3000", rec.Header().Get("Access-Control-Allow-Origin"), method)
		assert.Contains(t, rec.Header().Get("Access-Control-Allow-Methods"), method)
	}
}
