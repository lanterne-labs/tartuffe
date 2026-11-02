package providers

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

// GoogleName signs in with a Google account. The page asks Google for the
// scope "openid" alone, so the ID token names the account by an opaque
// identifier and carries no e-mail, name or photo: nothing about the person
// reaches the site. The identifier is stored as its HMAC under
// AUTH_SUBJECT_KEY, so the database alone cannot be matched with Google's.
const GoogleName = "google"

// googleCerts are the keys that sign Google's ID tokens.
const googleCerts = "https://www.googleapis.com/oauth2/v3/certs"

// Google checks the ID token the page got from Google (OpenID Connect
// implicit flow); its nonce is a challenge of the API.
type Google struct {
	// ClientID is the OAuth client of the site: the audience of the tokens.
	ClientID string
	// SubjectKey keys the HMAC of the stored identifier.
	SubjectKey []byte
	verifier   *oidc.IDTokenVerifier
}

// NewGoogleFromEnv reads GOOGLE_CLIENT_ID and AUTH_SUBJECT_KEY.
func NewGoogleFromEnv() (Provider, error) {
	id := os.Getenv("GOOGLE_CLIENT_ID")
	if id == "" {
		return nil, fmt.Errorf("GOOGLE_CLIENT_ID is required: the OAuth client ID of the site (Google Cloud console, Credentials)")
	}
	key := os.Getenv("AUTH_SUBJECT_KEY")
	if len(key) < 32 {
		return nil, fmt.Errorf("AUTH_SUBJECT_KEY is required: at least 32 random characters (openssl rand -hex 32); changing it gives every Google user a new account")
	}
	return NewGoogle(id, []byte(key), oidc.NewRemoteKeySet(context.Background(), googleCerts)), nil
}

// NewGoogle checks the tokens with the given keys.
func NewGoogle(clientID string, subjectKey []byte, keys oidc.KeySet) *Google {
	return &Google{
		ClientID:   clientID,
		SubjectKey: subjectKey,
		// Google writes its issuer with or without the scheme: Verify checks it.
		verifier: oidc.NewVerifier("https://accounts.google.com", keys, &oidc.Config{ClientID: clientID, SkipIssuerCheck: true}),
	}
}

func init() {
	Register(GoogleName, NewGoogleFromEnv)
}

func (g *Google) Name() string { return GoogleName }

func (g *Google) ChallengeTTL() time.Duration { return ChallengeTTL }

func (g *Google) Info() ProviderInfo {
	return ProviderInfo{Name: GoogleName, Role: RoleAccount, Config: map[string]any{"client_id": g.ClientID}}
}

// Verify reads {"challenge", "id_token"} (the challenge is already consumed
// by the API): the token must be Google's, for this site, unexpired, and
// carry the challenge as its nonce.
func (g *Google) Verify(ctx context.Context, payload json.RawMessage) (Identity, error) {
	var p struct {
		Challenge string `json:"challenge"`
		IDToken   string `json:"id_token"`
	}
	if err := json.Unmarshal(payload, &p); err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidProof, err)
	}
	if p.Challenge == "" || p.IDToken == "" {
		return Identity{}, fmt.Errorf("%w: challenge and id_token are required", ErrInvalidProof)
	}
	tok, err := g.verifier.Verify(ctx, p.IDToken)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: %v", ErrInvalidProof, err)
	}
	if tok.Issuer != "https://accounts.google.com" && tok.Issuer != "accounts.google.com" {
		return Identity{}, fmt.Errorf("%w: issuer %q is not Google", ErrInvalidProof, tok.Issuer)
	}
	if tok.Nonce != p.Challenge {
		return Identity{}, fmt.Errorf("%w: the token was not issued for this sign-in", ErrInvalidProof)
	}
	if tok.Subject == "" {
		return Identity{}, fmt.Errorf("%w: the token names no account", ErrInvalidProof)
	}
	mac := hmac.New(sha256.New, g.SubjectKey)
	mac.Write([]byte(tok.Subject))
	return Identity{Provider: GoogleName, Subject: hex.EncodeToString(mac.Sum(nil))}, nil
}
