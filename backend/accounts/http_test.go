package accounts_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/accounts/accountstest"
	"digital-mirror/backend/accounts/providers"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var do = accountstest.Do

// challengeProvider is a verification Challenger that accepts any payload
// naming the challenge it was given.
type challengeProvider struct{}

func (challengeProvider) Name() string { return "zkfake" }
func (challengeProvider) Info() providers.ProviderInfo {
	return providers.ProviderInfo{Name: "zkfake", Role: providers.RoleVerification, Verified: true}
}
func (challengeProvider) ChallengeTTL() time.Duration { return time.Minute }
func (challengeProvider) Verify(_ context.Context, payload json.RawMessage) (providers.Identity, error) {
	var p struct {
		Subject string `json:"subject"`
	}
	_ = json.Unmarshal(payload, &p)
	return providers.Identity{Provider: "zkfake", Subject: p.Subject}, nil
}

func newRouter(store *accountstest.MemStore, ps ...providers.Provider) http.Handler {
	return accountstest.Router(accounts.NewService(store, providers.NewRegistry(ps...)))
}

// TestChallengeFlow: a document proof needs a challenge it issued, once; a
// verification provider opens no account; the document kept is an HMAC of
// the provider's identifier, never the identifier.
func TestChallengeFlow(t *testing.T) {
	store := accountstest.NewMemStore()
	acc := accounts.NewService(store, providers.NewRegistry(providers.Noop{}, challengeProvider{}))
	acc.SubjectKey = []byte(strings.Repeat("k", 32))
	var documents []string
	h := accountstest.Router(acc, func(r chi.Router) {
		r.Post("/prove/{provider}", func(w http.ResponseWriter, r *http.Request) {
			doc, _, _, ok := acc.ProveDocument(w, r)
			if ok {
				documents = append(documents, doc)
				w.WriteHeader(http.StatusOK)
			}
		})
	})

	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/auth/challenge/noop", "", nil).Code, "noop takes no challenge")
	challenge := func() string {
		rec := do(t, h, http.MethodPost, "/api/auth/challenge/zkfake", "", nil)
		require.Equal(t, http.StatusOK, rec.Code)
		var c struct {
			Challenge string `json:"challenge"`
		}
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c))
		require.NotEmpty(t, c.Challenge)
		return c.Challenge
	}
	c := challenge()
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/auth/signin/zkfake", "", map[string]string{"subject": "s", "challenge": c}).Code, "a verification opens no account")

	prove := func(body map[string]string) int { return do(t, h, http.MethodPost, "/api/prove/zkfake", "", body).Code }
	assert.Equal(t, http.StatusBadRequest, prove(map[string]string{"subject": "s"}), "no challenge")
	assert.Equal(t, http.StatusBadRequest, prove(map[string]string{"subject": "s", "challenge": "forged"}))
	c = challenge()
	assert.Equal(t, http.StatusOK, prove(map[string]string{"subject": "s", "challenge": c}))
	assert.Equal(t, http.StatusBadRequest, prove(map[string]string{"subject": "s", "challenge": c}), "a challenge serves once")
	assert.Equal(t, http.StatusOK, prove(map[string]string{"subject": "s", "challenge": challenge()}))
	require.Len(t, documents, 2)
	assert.Equal(t, documents[0], documents[1], "one document, one identifier")
	assert.NotContains(t, documents[0], "s:")
	assert.Len(t, documents[0], 64)

	rec := do(t, h, http.MethodGet, "/api/auth/providers", "", nil)
	assert.JSONEq(t, `[{"name":"zkfake","role":"verification","verified":true},{"name":"noop","role":"test","verified":false}]`, rec.Body.String(), "verified providers first")
}

// TestDisplayName: the user sets their pseudonym, trimmed, 1 to 40
// characters.
func TestDisplayName(t *testing.T) {
	store := accountstest.NewMemStore()
	h := newRouter(store, providers.Noop{})
	alice := accountstest.SignIn(t, h, "alice")
	set := func(name string) *httptest.ResponseRecorder {
		return do(t, h, http.MethodPatch, "/api/auth/me", alice, map[string]string{"display_name": name})
	}
	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodPatch, "/api/auth/me", "", map[string]string{"display_name": "x"}).Code)
	rec := set("  Le   Citoyen ")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var me accounts.User
	require.NoError(t, json.Unmarshal(do(t, h, http.MethodGet, "/api/auth/me", alice, nil).Body.Bytes(), &me))
	assert.Equal(t, "Le Citoyen", me.DisplayName)
	assert.Equal(t, http.StatusBadRequest, set("   ").Code)
	assert.Equal(t, http.StatusBadRequest, set(strings.Repeat("é", 41)).Code)
}

// TestDisabledProvider: when a provider is turned off its sessions stop working.
func TestDisabledProvider(t *testing.T) {
	store := accountstest.NewMemStore()
	token := accountstest.SignIn(t, newRouter(store, providers.Noop{}), "alice")
	off := newRouter(store)
	assert.Equal(t, http.StatusUnauthorized, do(t, off, http.MethodGet, "/api/auth/me", token, nil).Code)
	assert.Equal(t, http.StatusNotFound, do(t, off, http.MethodPost, "/api/auth/signin/noop", "", map[string]string{"pseudonym": "x"}).Code)
}

// TestSignOutAndDelete: signing out ends the session; deleting the account
// starts the next sign-in from a new account.
func TestSignOutAndDelete(t *testing.T) {
	store := accountstest.NewMemStore()
	h := newRouter(store, providers.Noop{})

	alice := accountstest.SignIn(t, h, "alice")
	assert.Equal(t, http.StatusNoContent, do(t, h, http.MethodPost, "/api/auth/signout", alice, nil).Code)
	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodGet, "/api/auth/me", alice, nil).Code)

	alice = accountstest.SignIn(t, h, "alice")
	id := store.UserOfToken(alice)
	assert.Equal(t, http.StatusNoContent, do(t, h, http.MethodDelete, "/api/auth/me", alice, nil).Code)
	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodGet, "/api/auth/me", alice, nil).Code)
	again := accountstest.SignIn(t, h, "alice")
	assert.NotEqual(t, id, store.UserOfToken(again))
}
