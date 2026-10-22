package providers

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/go-jose/go-jose/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGoogleVerify: an ID token of Google for the site, unexpired and with
// the challenge as nonce, names an account by the HMAC of its subject;
// anything else is an invalid proof.
func TestGoogleVerify(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: key}, nil)
	require.NoError(t, err)
	g := NewGoogle("client-1", []byte("0123456789abcdef0123456789abcdef"), &oidc.StaticKeySet{PublicKeys: []crypto.PublicKey{&key.PublicKey}})

	token := func(claims map[string]any) string {
		base := map[string]any{"iss": "https://accounts.google.com", "aud": "client-1", "sub": "1234567890",
			"nonce": "c1", "iat": time.Now().Unix(), "exp": time.Now().Add(time.Hour).Unix()}
		for k, v := range claims {
			if v == nil {
				delete(base, k)
			} else {
				base[k] = v
			}
		}
		b, err := json.Marshal(base)
		require.NoError(t, err)
		obj, err := signer.Sign(b)
		require.NoError(t, err)
		s, err := obj.CompactSerialize()
		require.NoError(t, err)
		return s
	}
	verify := func(challenge, tok string) (Identity, error) {
		p, _ := json.Marshal(map[string]string{"challenge": challenge, "id_token": tok})
		return g.Verify(context.Background(), p)
	}

	id, err := verify("c1", token(nil))
	require.NoError(t, err)
	assert.Equal(t, GoogleName, id.Provider)
	assert.Len(t, id.Subject, 64, "the HMAC, not the Google identifier")
	assert.NotContains(t, id.Subject, "1234567890")
	again, err := verify("c1", token(map[string]any{"iss": "accounts.google.com"}))
	require.NoError(t, err, "Google's issuer without the scheme")
	assert.Equal(t, id.Subject, again.Subject, "one account, one subject")
	other, err := verify("c1", token(map[string]any{"sub": "999"}))
	require.NoError(t, err)
	assert.NotEqual(t, id.Subject, other.Subject)

	for name, tok := range map[string]string{
		"another site":   token(map[string]any{"aud": "client-2"}),
		"another issuer": token(map[string]any{"iss": "https://evil.example"}),
		"expired":        token(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}),
		"no subject":     token(map[string]any{"sub": nil}),
		"other nonce":    token(map[string]any{"nonce": "c2"}),
		"not a token":    "nope",
	} {
		_, err := verify("c1", tok)
		assert.True(t, errors.Is(err, ErrInvalidProof), "%s: %v", name, err)
	}
	other2, _ := rsa.GenerateKey(rand.Reader, 2048)
	forger, _ := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: other2}, nil)
	b, _ := json.Marshal(map[string]any{"iss": "https://accounts.google.com", "aud": "client-1", "sub": "1", "nonce": "c1", "exp": time.Now().Add(time.Hour).Unix()})
	obj, _ := forger.Sign(b)
	forged, _ := obj.CompactSerialize()
	_, err = verify("c1", forged)
	assert.ErrorIs(t, err, ErrInvalidProof, "signed by another key")
	_, err = verify("", token(nil))
	assert.ErrorIs(t, err, ErrInvalidProof, "no challenge")
}
