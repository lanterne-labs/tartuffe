package providers

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubVerifier writes a shell script that records its stdin and prints out,
// and returns a provider that runs it in place of node verify.mjs.
func stubVerifier(t *testing.T, out string) (*ZKPassport, string) {
	t.Helper()
	dir := t.TempDir()
	seen := filepath.Join(dir, "stdin.json")
	script := filepath.Join(dir, "verify.sh")
	require.NoError(t, os.WriteFile(script, []byte("cat > "+seen+"\necho '"+out+"'\n"), 0o755))
	return &ZKPassport{Domain: "tartuffe.me", Scope: "tartuffe-voter", MinAge: 18, Nationality: "FRA", Node: "/bin/sh", Script: script}, seen
}

// TestZKPassportVerify: the verifier gets the server's own settings with the
// browser's proofs, and its unique identifier becomes the subject.
func TestZKPassportVerify(t *testing.T) {
	z, seen := stubVerifier(t, `{"verified":true,"uniqueIdentifier":"0xabc","errors":null}`)
	id, err := z.Verify(context.Background(), json.RawMessage(`{"challenge":"c1","proofs":[{"proof":"p"}],"result":{"age":{}},"domain":"evil.example"}`))
	require.NoError(t, err)
	assert.Equal(t, Identity{Provider: ZKPassportName, Subject: "0xabc"}, id)

	var in map[string]any
	raw, err := os.ReadFile(seen)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &in))
	assert.Equal(t, "tartuffe.me", in["domain"], "the domain is the server's, never the browser's")
	assert.Equal(t, "c1", in["challenge"])
	assert.Equal(t, "FRA", in["nationality"])
	assert.EqualValues(t, 18, in["minAge"])
	assert.Equal(t, "tartuffe-voter", in["scope"])
}

func TestZKPassportRefused(t *testing.T) {
	z, _ := stubVerifier(t, `{"verified":false,"uniqueIdentifier":"","errors":{"message":"bad proof"}}`)
	_, err := z.Verify(context.Background(), json.RawMessage(`{"challenge":"c1","proofs":[{}],"result":{}}`))
	assert.True(t, errors.Is(err, ErrInvalidProof), "%v", err)
	assert.Contains(t, err.Error(), "bad proof")

	_, err = z.Verify(context.Background(), json.RawMessage(`{"proofs":[{}],"result":{}}`))
	assert.True(t, errors.Is(err, ErrInvalidProof), "no challenge")

	broken, _ := stubVerifier(t, `not json`)
	_, err = broken.Verify(context.Background(), json.RawMessage(`{"challenge":"c1","proofs":[{}],"result":{}}`))
	require.Error(t, err)
	assert.False(t, errors.Is(err, ErrInvalidProof), "a broken verifier is a server failure")
}

func TestNoopVerify(t *testing.T) {
	id, err := Noop{}.Verify(context.Background(), json.RawMessage(`{"pseudonym":"  Alice "}`))
	require.NoError(t, err)
	assert.Equal(t, Identity{Provider: NoopName, Subject: "alice", DisplayName: "Alice"}, id)
	_, err = Noop{}.Verify(context.Background(), json.RawMessage(`{"pseudonym":"  "}`))
	assert.True(t, errors.Is(err, ErrInvalidProof))
}

func TestRegistryFromEnv(t *testing.T) {
	t.Setenv("AUTH_PROVIDERS", "noop")
	r, err := RegistryFromEnv()
	require.NoError(t, err)
	_, ok := r.Get(NoopName)
	assert.True(t, ok)

	t.Setenv("AUTH_PROVIDERS", "nope")
	_, err = RegistryFromEnv()
	assert.Error(t, err)

	t.Setenv("AUTH_PROVIDERS", "zkpassport")
	t.Setenv("ZKPASSPORT_DOMAIN", "")
	_, err = RegistryFromEnv()
	assert.ErrorContains(t, err, "ZKPASSPORT_DOMAIN")
}
