package xbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type memTokens struct{ t *storage.XOAuth }

func (m *memTokens) GetXOAuth(context.Context) (*storage.XOAuth, error) { return m.t, nil }
func (m *memTokens) SaveXOAuth(_ context.Context, t storage.XOAuth) error {
	m.t = &t
	return nil
}

// TestOAuth2Poster: the first post renews from the seed and stores the new
// refresh token; the next reuses the access token; an expired or refused one
// is renewed with the stored refresh token, never the seed again; a new seed
// starts over.
func TestOAuth2Poster(t *testing.T) {
	var refreshes []string
	var bearers []string
	n := 0
	reject := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			id, secret, ok := r.BasicAuth()
			assert.True(t, ok)
			assert.Equal(t, "cid", id)
			assert.Equal(t, "csecret", secret)
			require.NoError(t, r.ParseForm())
			assert.Equal(t, "refresh_token", r.Form.Get("grant_type"))
			refreshes = append(refreshes, r.Form.Get("refresh_token"))
			n++
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at" + string(rune('0'+n)), "refresh_token": "rt" + string(rune('0'+n)), "expires_in": 7200})
			return
		}
		bearers = append(bearers, r.Header.Get("Authorization"))
		if reject {
			reject = false
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var body map[string]string
		require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"77"}}`))
	}))
	defer srv.Close()
	store := &memTokens{}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	p := NewOAuth2Poster("cid", "csecret", "seed", store, srv.URL+"/tweets", srv.URL+"/token")
	p.now = func() time.Time { return now }
	ctx := context.Background()

	id, err := p.Post(ctx, "Bonjour", "")
	require.NoError(t, err)
	assert.Equal(t, "77", id)
	assert.Equal(t, []string{"seed"}, refreshes)
	assert.Equal(t, "rt1", store.t.RefreshToken)

	_, err = p.Post(ctx, "Encore", "5")
	require.NoError(t, err)
	assert.Len(t, refreshes, 1, "the access token is reused while it lasts")

	now = now.Add(2 * time.Hour)
	_, err = p.Post(ctx, "Plus tard", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"seed", "rt1"}, refreshes)

	reject = true
	_, err = p.Post(ctx, "Refusé une fois", "")
	require.NoError(t, err)
	assert.Equal(t, []string{"seed", "rt1", "rt2"}, refreshes)
	assert.Equal(t, "Bearer at3", bearers[len(bearers)-1])

	// A restart keeps the stored tokens; a new seed starts again from it.
	p2 := NewOAuth2Poster("cid", "csecret", "seed", store, srv.URL+"/tweets", srv.URL+"/token")
	p2.now = p.now
	_, err = p2.Post(ctx, "Après un redémarrage", "")
	require.NoError(t, err)
	assert.Len(t, refreshes, 3)
	p3 := NewOAuth2Poster("cid", "csecret", "seed2", store, srv.URL+"/tweets", srv.URL+"/token")
	p3.now = p.now
	_, err = p3.Post(ctx, "Nouveau jeton", "")
	require.NoError(t, err)
	assert.Equal(t, "seed2", refreshes[len(refreshes)-1])
}
