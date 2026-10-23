// Package accountstest helps the tests of the parts that need signed-in
// users: an in-memory accounts store, a router with the accounts' routes,
// and the request to sign in with the noop provider.
package accountstest

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/accounts/providers"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

// MemStore keeps the accounts in memory.
type MemStore struct {
	mu         sync.Mutex
	users      map[string]*accounts.User // by provider/subject
	sessions   map[string]int64          // by token hash
	challenges map[string]string         // challenge -> provider
	next       int64
}

// NewMemStore is an empty store.
func NewMemStore() *MemStore {
	return &MemStore{users: map[string]*accounts.User{}, sessions: map[string]int64{}, challenges: map[string]string{}}
}

func (m *MemStore) user(id int64) *accounts.User {
	for _, u := range m.users {
		if u.ID == id {
			return u
		}
	}
	return nil
}

// UserOfToken is the account of a session token (0 when none).
func (m *MemStore) UserOfToken(token string) int64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[string(providers.HashToken(token))]
}

func (m *MemStore) UpsertUser(_ context.Context, provider, subject, name string) (*accounts.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := provider + "/" + subject
	if u, ok := m.users[key]; ok {
		c := *u
		return &c, nil
	}
	m.next++
	u := &accounts.User{ID: m.next, Provider: provider, DisplayName: name}
	m.users[key] = u
	c := *u
	return &c, nil
}

func (m *MemStore) CreateSession(_ context.Context, userID int64, hash []byte, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[string(hash)] = userID
	return nil
}

func (m *MemStore) SessionUser(_ context.Context, hash []byte) (*accounts.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.sessions[string(hash)]
	if !ok {
		return nil, nil
	}
	if u := m.user(id); u != nil {
		c := *u
		return &c, nil
	}
	return nil, nil
}

func (m *MemStore) DeleteSession(_ context.Context, hash []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, string(hash))
	return nil
}

func (m *MemStore) CreateChallenge(_ context.Context, provider, challenge string, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.challenges[challenge] = provider
	return nil
}

func (m *MemStore) ConsumeChallenge(_ context.Context, provider, challenge string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.challenges[challenge] != provider {
		return false, nil
	}
	delete(m.challenges, challenge)
	return true, nil
}

func (m *MemStore) SetDisplayName(_ context.Context, userID int64, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u := m.user(userID); u != nil {
		u.DisplayName = name
	}
	return nil
}

func (m *MemStore) DeleteUser(_ context.Context, userID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, u := range m.users {
		if u.ID == userID {
			delete(m.users, k)
		}
	}
	for h, id := range m.sessions {
		if id == userID {
			delete(m.sessions, h)
		}
	}
	return nil
}

func (m *MemStore) PurgeUsers(_ context.Context, provider string) (int64, error) {
	m.mu.Lock()
	var ids []int64
	for _, u := range m.users {
		if u.Provider == provider {
			ids = append(ids, u.ID)
		}
	}
	m.mu.Unlock()
	for _, id := range ids {
		_ = m.DeleteUser(context.Background(), id)
	}
	return int64(len(ids)), nil
}

// NoLimit is a sign-in limit that lets everything through.
func NoLimit(next http.Handler) http.Handler { return next }

// Router mounts the accounts' routes and the others under /api.
func Router(acc *accounts.Service, mount ...func(chi.Router)) http.Handler {
	r := chi.NewRouter()
	r.Route("/api", func(r chi.Router) {
		acc.Routes(r, NoLimit)
		for _, m := range mount {
			m(r)
		}
	})
	return r
}

// Do sends a request with the JSON body and the session token.
func Do(t *testing.T, h http.Handler, method, path, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		require.NoError(t, json.NewEncoder(&buf).Encode(body))
	}
	req := httptest.NewRequest(method, path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// SignIn opens a noop account of the pseudonym and returns its session
// token.
func SignIn(t *testing.T, h http.Handler, pseudonym string) string {
	t.Helper()
	rec := Do(t, h, http.MethodPost, "/api/auth/signin/noop", "", map[string]string{"pseudonym": pseudonym})
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.Token)
	return resp.Token
}
