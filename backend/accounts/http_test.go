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

// TestChallengeFlow: a verification needs a challenge it issued, once; a
// verification provider opens no account.
func TestChallengeFlow(t *testing.T) {
	store := accountstest.NewMemStore()
	h := newRouter(store, providers.Noop{}, challengeProvider{})

	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/auth/challenge/noop", "", nil).Code, "noop takes no challenge")
	rec := do(t, h, http.MethodPost, "/api/auth/challenge/zkfake", "", nil)
	require.Equal(t, http.StatusOK, rec.Code)
	var c struct {
		Challenge string `json:"challenge"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &c))
	require.NotEmpty(t, c.Challenge)

	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/auth/signin/zkfake", "", map[string]string{"subject": "s", "challenge": c.Challenge}).Code, "a verification opens no account")

	alice := accountstest.SignInUnverified(t, h, "alice")
	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodPost, "/api/me/verify/zkfake", "", map[string]string{"subject": "s", "challenge": c.Challenge}).Code)
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/me/verify/zkfake", alice, map[string]string{"subject": "s"}).Code, "no challenge")
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/me/verify/zkfake", alice, map[string]string{"subject": "s", "challenge": "forged"}).Code)
	ok := do(t, h, http.MethodPost, "/api/me/verify/zkfake", alice, map[string]string{"subject": "s", "challenge": c.Challenge})
	assert.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	assert.Equal(t, http.StatusBadRequest, do(t, h, http.MethodPost, "/api/me/verify/zkfake", alice, map[string]string{"subject": "s", "challenge": c.Challenge}).Code, "a challenge serves once")
	assert.Equal(t, "zkfake:s", store.Subject(store.UserOfToken(alice)), "the subject is namespaced by its provider")

	rec = do(t, h, http.MethodGet, "/api/auth/providers", "", nil)
	assert.JSONEq(t, `[{"name":"zkfake","role":"verification","verified":true},{"name":"noop","role":"test","verified":false}]`, rec.Body.String(), "verified providers first")
}

// TestVerification: a document shown on another account moves there only
// once the user confirms; an account keeps its document.
func TestVerification(t *testing.T) {
	store := accountstest.NewMemStore()
	var moved [][2]int64
	store.OnMoved = func(from, to int64) { moved = append(moved, [2]int64{from, to}) }
	h := newRouter(store, providers.Noop{})
	verify := func(token, doc string) map[string]any {
		rec := do(t, h, http.MethodPost, "/api/me/verify/noop", token, map[string]string{"pseudonym": doc})
		var out map[string]any
		require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out), rec.Body.String())
		out["status"] = float64(rec.Code)
		return out
	}

	first := accountstest.SignInUnverified(t, h, "first")
	var me accounts.User
	require.NoError(t, json.Unmarshal(do(t, h, http.MethodGet, "/api/auth/me", first, nil).Body.Bytes(), &me))
	assert.False(t, me.Verified)

	assert.Equal(t, float64(http.StatusOK), verify(first, "doc")["status"])
	assert.Equal(t, float64(http.StatusOK), verify(first, "doc")["status"], "the same document again")
	other := verify(first, "other")
	assert.Equal(t, float64(http.StatusConflict), other["status"])
	assert.Equal(t, "other_document", other["code"])

	second := accountstest.SignInUnverified(t, h, "second")
	held := verify(second, "doc")
	assert.Equal(t, float64(http.StatusConflict), held["status"])
	assert.Equal(t, "document_elsewhere", held["code"])
	token, _ := held["move_token"].(string)
	require.NotEmpty(t, token)
	assert.Empty(t, moved, "nothing moves before the user confirms")
	assert.True(t, store.Verified(store.UserOfToken(first)))

	confirm := func(as, tok string) *httptest.ResponseRecorder {
		return do(t, h, http.MethodPost, "/api/me/verify/move", as, map[string]string{"move_token": tok})
	}
	assert.Equal(t, http.StatusBadRequest, confirm(first, token).Code, "another account's move")
	assert.Equal(t, http.StatusBadRequest, confirm(second, "forged").Code)
	rec := confirm(second, token)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"moved":true`)
	assert.Equal(t, http.StatusBadRequest, confirm(second, token).Code, "a move serves once")
	assert.Equal(t, [][2]int64{{store.UserOfToken(first), store.UserOfToken(second)}}, moved, "the hooks follow the move")
	assert.False(t, store.Verified(store.UserOfToken(first)))
	require.NoError(t, json.Unmarshal(do(t, h, http.MethodGet, "/api/auth/me", second, nil).Body.Bytes(), &me))
	assert.True(t, me.Verified)
}

// TestDisplayName: the user sets their pseudonym, trimmed, 1 to 40
// characters.
func TestDisplayName(t *testing.T) {
	store := accountstest.NewMemStore()
	h := newRouter(store, providers.Noop{})
	alice := accountstest.SignInUnverified(t, h, "alice")
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
// runs the hooks and starts the next sign-in from an empty account.
func TestSignOutAndDelete(t *testing.T) {
	store := accountstest.NewMemStore()
	var deleted []int64
	store.OnDeleting = func(id int64) { deleted = append(deleted, id) }
	acc := accounts.NewService(store, providers.NewRegistry(providers.Noop{}))
	changed := 0
	acc.Changed = append(acc.Changed, func() { changed++ })
	h := accountstest.Router(acc)

	alice := accountstest.SignIn(t, h, "alice")
	assert.Equal(t, http.StatusNoContent, do(t, h, http.MethodPost, "/api/auth/signout", alice, nil).Code)
	assert.Equal(t, http.StatusUnauthorized, do(t, h, http.MethodGet, "/api/auth/me", alice, nil).Code)

	alice = accountstest.SignIn(t, h, "alice")
	id := store.UserOfToken(alice)
	assert.Equal(t, http.StatusNoContent, do(t, h, http.MethodDelete, "/api/auth/me", alice, nil).Code)
	assert.Equal(t, []int64{id}, deleted)
	assert.Equal(t, 1, changed, "the other parts hear of it")
	again := accountstest.SignInUnverified(t, h, "alice")
	assert.NotEqual(t, id, store.UserOfToken(again))
	assert.False(t, store.Verified(store.UserOfToken(again)))
}
