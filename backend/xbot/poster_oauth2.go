package xbot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"digital-mirror/backend/storage"

	"github.com/rs/zerolog/log"
)

// TokenStore keeps the OAuth 2.0 tokens between renewals (x_oauth).
type TokenStore interface {
	GetXOAuth(ctx context.Context) (*storage.XOAuth, error)
	SaveXOAuth(ctx context.Context, t storage.XOAuth) error
}

// OAuth2Poster posts with the official X API as the account that granted
// the app tweet.write and offline.access (OAuth 2.0, user context). An access
// token lasts two hours; each renewal gives a new refresh token and ends the
// old one, so the tokens live in the store, started from seed.
type OAuth2Poster struct {
	clientID, clientSecret, seed string
	endpoint, tokenEndpoint      string
	store                        TokenStore
	client                       *http.Client
	now                          func() time.Time
	mu                           sync.Mutex
}

// NewPosterFromEnv picks how the bot posts: the OAuth 1.0a keys when set
// (X_API_KEY, X_API_SECRET, X_ACCESS_TOKEN, X_ACCESS_SECRET), else OAuth 2.0
// (X_CLIENT_ID, X_CLIENT_SECRET, X_REFRESH_TOKEN: the refresh token the app
// was first given; a new value starts again from it). Nil without either:
// the bot then drafts and asks, but posts nothing.
func NewPosterFromEnv(store TokenStore) Poster {
	if p := NewXAPIPosterFromEnv(); p != nil {
		return p
	}
	id, secret, seed := os.Getenv("X_CLIENT_ID"), os.Getenv("X_CLIENT_SECRET"), os.Getenv("X_REFRESH_TOKEN")
	if id == "" || secret == "" || seed == "" {
		return nil
	}
	return NewOAuth2Poster(id, secret, seed, store, "https://api.x.com/2/tweets", "https://api.x.com/2/oauth2/token")
}

// NewOAuth2Poster posts to endpoint and renews at tokenEndpoint (test
// servers in the tests).
func NewOAuth2Poster(clientID, clientSecret, seed string, store TokenStore, endpoint, tokenEndpoint string) *OAuth2Poster {
	return &OAuth2Poster{clientID: clientID, clientSecret: clientSecret, seed: seed, store: store,
		endpoint: endpoint, tokenEndpoint: tokenEndpoint, client: &http.Client{Timeout: 30 * time.Second}, now: time.Now}
}

// accessToken returns a valid access token, renewing it when it is about to
// end or when force is set (X refused it).
func (p *OAuth2Poster) accessToken(ctx context.Context, force bool) (string, error) {
	t, err := p.store.GetXOAuth(ctx)
	if err != nil {
		return "", err
	}
	if t == nil || t.Seed != p.seed {
		t = &storage.XOAuth{Seed: p.seed, RefreshToken: p.seed}
	}
	if !force && t.AccessToken != "" && p.now().Before(t.ExpiresAt.Add(-2*time.Minute)) {
		return t.AccessToken, nil
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.tokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetBasicAuth(p.clientID, p.clientSecret)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("renew the X token: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var out struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int    `json:"expires_in"`
		Error        string `json:"error"`
		Description  string `json:"error_description"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || resp.StatusCode != http.StatusOK || out.AccessToken == "" {
		return "", fmt.Errorf("renew the X token: status %d: %s %s (a refused refresh token needs a new X_REFRESH_TOKEN)", resp.StatusCode, out.Error, out.Description)
	}
	t.AccessToken = out.AccessToken
	t.ExpiresAt = p.now().Add(time.Duration(out.ExpiresIn) * time.Second)
	if out.RefreshToken != "" {
		t.RefreshToken = out.RefreshToken
	}
	if err := p.store.SaveXOAuth(ctx, *t); err != nil {
		// X ended the old refresh token: the new one exists only here.
		log.Error().Err(err).Msg("xbot: the renewed X tokens could not be stored; the next renewal will fail and need a new X_REFRESH_TOKEN")
		return "", err
	}
	return t.AccessToken, nil
}

var errUnauthorized = errors.New("unauthorized")

func (p *OAuth2Poster) Post(ctx context.Context, text, quote string) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	token, err := p.accessToken(ctx, false)
	if err != nil {
		return "", err
	}
	id, err := p.post(ctx, token, text, quote)
	if errors.Is(err, errUnauthorized) {
		if token, err = p.accessToken(ctx, true); err != nil {
			return "", err
		}
		id, err = p.post(ctx, token, text, quote)
	}
	return id, err
}

func (p *OAuth2Poster) post(ctx context.Context, token, text, quote string) (string, error) {
	payload := map[string]string{"text": text}
	if quote != "" {
		payload["quote_tweet_id"] = quote
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("post tweet: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode == http.StatusUnauthorized {
		return "", fmt.Errorf("post tweet: status 401: %s: %w", strings.TrimSpace(string(raw)), errUnauthorized)
	}
	if resp.StatusCode == http.StatusForbidden && quote != "" && quoteRefused(string(raw)) {
		return "", fmt.Errorf("post tweet: status 403: %s: %w", strings.TrimSpace(string(raw)), ErrQuoteRefused)
	}
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("post tweet: status %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out struct {
		Data struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil || out.Data.ID == "" {
		return "", fmt.Errorf("post tweet: unexpected reply %s", strings.TrimSpace(string(raw)))
	}
	return out.Data.ID, nil
}
