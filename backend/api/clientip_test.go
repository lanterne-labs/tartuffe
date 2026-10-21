package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"digital-mirror/backend/auth"

	"github.com/stretchr/testify/assert"
)

func TestKeyByClientIP(t *testing.T) {
	req := func(remote, realIP string) *http.Request {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.RemoteAddr = remote
		if realIP != "" {
			r.Header.Set("X-Real-IP", realIP)
		}
		return r
	}
	direct, proxied := keyByClientIP(""), keyByClientIP("X-Real-IP")

	k, _ := direct(req("100.64.0.2:4000", "203.0.113.9"))
	assert.Equal(t, "100.64.0.2", k, "without a proxy the header is the client's own claim")
	k, _ = proxied(req("100.64.0.2:4000", "203.0.113.9"))
	assert.Equal(t, "203.0.113.9", k)
	k, _ = proxied(req("100.64.0.2:4000", "not an ip"))
	assert.Equal(t, "100.64.0.2", k)
	k, _ = proxied(req("100.64.0.2:4000", "2001:db8:1:2:3:4:5:6"))
	assert.Equal(t, "2001:db8:1:2::/64", k)
	k2, _ := proxied(req("100.64.0.2:4000", "2001:db8:1:2:ffff::1"))
	assert.Equal(t, k, k2, "one /64, one limit")
}

func TestClientIPHeader(t *testing.T) {
	t.Setenv("CLIENT_IP_HEADER", "")
	t.Setenv("DM_PRODUCTION", "")
	assert.Equal(t, "", clientIPHeader())
	t.Setenv("DM_PRODUCTION", "production")
	assert.Equal(t, "X-Real-IP", clientIPHeader())
	t.Setenv("CLIENT_IP_HEADER", "none")
	assert.Equal(t, "", clientIPHeader())
	t.Setenv("CLIENT_IP_HEADER", "Fly-Client-IP")
	assert.Equal(t, "Fly-Client-IP", clientIPHeader())
}

// TestSignInRateLimit: the challenges and sign-ins of one client share a
// tight limit; another client and the other routes are not held by it.
func TestSignInRateLimit(t *testing.T) {
	t.Setenv("SIGNIN_RATE_LIMIT_PER_MINUTE", "3")
	t.Setenv("DM_PRODUCTION", "production")
	t.Setenv("CLIENT_IP_HEADER", "")
	h := NewServer(pollFixture(), auth.NewRegistry(auth.Noop{})).Handler()
	post := func(path, ip string) int {
		r := httptest.NewRequest(http.MethodPost, path, nil)
		r.Header.Set("X-Real-IP", ip)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		return rec.Code
	}
	for range 3 {
		assert.NotEqual(t, http.StatusTooManyRequests, post("/api/auth/signin/noop", "203.0.113.1"))
	}
	assert.Equal(t, http.StatusTooManyRequests, post("/api/auth/challenge/noop", "203.0.113.1"))
	assert.NotEqual(t, http.StatusTooManyRequests, post("/api/auth/signin/noop", "203.0.113.2"))

	r := httptest.NewRequest(http.MethodGet, "/api/polls/presidentielle-2027", nil)
	r.Header.Set("X-Real-IP", "203.0.113.1")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	assert.Equal(t, http.StatusOK, rec.Code)
}

// TestRateLimitKeepsCORS: a request over the limit gets a 429 the page can
// read, with its CORS headers.
func TestRateLimitKeepsCORS(t *testing.T) {
	t.Setenv("RATE_LIMIT_PER_MINUTE", "2")
	t.Setenv("CLIENT_IP_HEADER", "none")
	h := NewServer(pollFixture(), nil).Handler()
	var rec *httptest.ResponseRecorder
	for range 3 {
		r := httptest.NewRequest(http.MethodGet, "/api/polls", nil)
		r.Header.Set("Origin", "http://localhost:3000")
		rec = httptest.NewRecorder()
		h.ServeHTTP(rec, r)
	}
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "http://localhost:3000", rec.Header().Get("Access-Control-Allow-Origin"))
}
