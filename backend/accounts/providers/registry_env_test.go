package providers

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The noop provider (a sign-in without proof) is for local development:
// in production it is refused, and nothing is enabled unless named.
func TestRegistryFromEnvRefusesNoopInProduction(t *testing.T) {
	t.Setenv("DM_PRODUCTION", "production")
	t.Setenv("AUTH_PROVIDERS", "noop")
	_, err := RegistryFromEnv()
	require.ErrorContains(t, err, "noop")
	t.Setenv("AUTH_PROVIDERS", "")
	r, err := RegistryFromEnv()
	require.NoError(t, err)
	require.Empty(t, r.Infos())
	t.Setenv("DM_PRODUCTION", "")
	os.Unsetenv("AUTH_PROVIDERS")
	r, err = RegistryFromEnv()
	require.NoError(t, err)
	require.Len(t, r.Infos(), 1)
	require.Equal(t, NoopName, r.Infos()[0].Name)
}

// ZKPassport's mock documents are for local development: on the host the
// provider refuses to start with them.
func TestZKPassportRefusesDevModeInProduction(t *testing.T) {
	t.Setenv("ZKPASSPORT_DOMAIN", "tartuffe.me")
	t.Setenv("ZKPASSPORT_DEV_MODE", "true")
	t.Setenv("DM_PRODUCTION", "production")
	_, err := NewZKPassportFromEnv()
	require.ErrorContains(t, err, "never in production")
}
