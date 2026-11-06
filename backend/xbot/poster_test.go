package xbot

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOAuthSignature: the worked example of X's documentation ("Creating a
// signature").
func TestOAuthSignature(t *testing.T) {
	p := NewXAPIPoster("xvz1evFS4wEEPTGEFPHBog", "kAcSOqF21Fu85e7zjz7ZN2U4ZRhfV3WpwPAoE3Z7kBw",
		"370773112-GmHxMAgYyLbNEtIKZeRNFsMKPR9EyMZeS9weJAEb", "LswwdoUaIvS8ltyTt5jkRh4J50vUPVVHtR2YPi5kE", "")
	p.now = func() time.Time { return time.Unix(1318622958, 0) }
	p.nonce = func() string { return "kYjzVBB8Y0ZFabxSWbWovY3uYSQ2pTgmZeNu2VS4cg" }
	h := p.authorization("POST", "https://api.twitter.com/1.1/statuses/update.json", map[string]string{
		"include_entities": "true", "status": "Hello Ladies + Gentlemen, a signed OAuth request!"})
	assert.Contains(t, h, `oauth_signature="hCtSmYh%2BiHYCEqBWrE7C7hYmtUk%3D"`)
	assert.True(t, strings.HasPrefix(h, "OAuth "))
	assert.NotContains(t, h, "status=", "the request's own parameters are signed, not sent in the header")
}

func TestPost(t *testing.T) {
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.True(t, strings.HasPrefix(r.Header.Get("Authorization"), "OAuth "))
		require.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"data":{"id":"1850","text":"x"}}`))
	}))
	defer srv.Close()
	id, err := NewXAPIPoster("k", "s", "t", "ts", srv.URL).Post(context.Background(), "Bonjour")
	require.NoError(t, err)
	assert.Equal(t, "1850", id)
	assert.Equal(t, "Bonjour", got["text"])

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"detail":"duplicate content"}`))
	}))
	defer bad.Close()
	_, err = NewXAPIPoster("k", "s", "t", "ts", bad.URL).Post(context.Background(), "Bonjour")
	assert.ErrorContains(t, err, "status 403")
}
