package xbot

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Poster posts a tweet and returns its id.
type Poster interface {
	Post(ctx context.Context, text string) (string, error)
}

// XAPIPoster posts with the official X API (pay per use), as the account
// whose app keys and access token it holds (OAuth 1.0a, user context).
type XAPIPoster struct {
	consumerKey, consumerSecret, token, tokenSecret string
	endpoint                                        string
	client                                          *http.Client
	now                                             func() time.Time
	nonce                                           func() string
}

// NewXAPIPosterFromEnv reads X_API_KEY, X_API_SECRET, X_ACCESS_TOKEN and
// X_ACCESS_SECRET (the app of the bot's account, "Read and write"). Nil when
// unset: the bot then drafts and asks, but posts nothing.
func NewXAPIPosterFromEnv() *XAPIPoster {
	k, s, t, ts := os.Getenv("X_API_KEY"), os.Getenv("X_API_SECRET"), os.Getenv("X_ACCESS_TOKEN"), os.Getenv("X_ACCESS_SECRET")
	if k == "" || s == "" || t == "" || ts == "" {
		return nil
	}
	return NewXAPIPoster(k, s, t, ts, "https://api.x.com/2/tweets")
}

// NewXAPIPoster posts to endpoint (a test server in the tests).
func NewXAPIPoster(consumerKey, consumerSecret, token, tokenSecret, endpoint string) *XAPIPoster {
	return &XAPIPoster{consumerKey: consumerKey, consumerSecret: consumerSecret, token: token, tokenSecret: tokenSecret,
		endpoint: endpoint, client: &http.Client{Timeout: 30 * time.Second}, now: time.Now,
		nonce: func() string {
			b := make([]byte, 16)
			_, _ = rand.Read(b)
			return hex.EncodeToString(b)
		}}
}

// oauthEscape is RFC 3986 percent-encoding, as OAuth 1.0a signs.
func oauthEscape(s string) string {
	return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
}

// authorization is the OAuth 1.0a header of a request. A JSON body is not
// signed: only the oauth_ parameters and the query or form parameters given
// in signed (none for a tweet).
func (p *XAPIPoster) authorization(method, endpoint string, signed map[string]string) string {
	params := map[string]string{
		"oauth_consumer_key":     p.consumerKey,
		"oauth_nonce":            p.nonce(),
		"oauth_signature_method": "HMAC-SHA1",
		"oauth_timestamp":        strconv.FormatInt(p.now().Unix(), 10),
		"oauth_token":            p.token,
		"oauth_version":          "1.0",
	}
	all := map[string]string{}
	for k, v := range params {
		all[k] = v
	}
	for k, v := range signed {
		all[k] = v
	}
	keys := make([]string, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	pairs := make([]string, len(keys))
	for i, k := range keys {
		pairs[i] = oauthEscape(k) + "=" + oauthEscape(all[k])
	}
	base := method + "&" + oauthEscape(endpoint) + "&" + oauthEscape(strings.Join(pairs, "&"))
	mac := hmac.New(sha1.New, []byte(oauthEscape(p.consumerSecret)+"&"+oauthEscape(p.tokenSecret)))
	mac.Write([]byte(base))
	params["oauth_signature"] = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	keys = keys[:0]
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf(`%s="%s"`, oauthEscape(k), oauthEscape(params[k]))
	}
	return "OAuth " + strings.Join(parts, ", ")
}

func (p *XAPIPoster) Post(ctx context.Context, text string) (string, error) {
	body, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", p.authorization(http.MethodPost, p.endpoint, nil))
	resp, err := p.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("post tweet: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
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
