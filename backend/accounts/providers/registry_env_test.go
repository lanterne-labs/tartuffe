package providers

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// The noop provider (a sign-in without proof) is for local development:
// on the host it is refused, and nothing is enabled unless named.
func TestRegistryFromEnvRefusesNoopOnthe host(t *testing.T) {
	t.Setenv("DM_PRODUCTION", "production")
	t.Setenv("AUTH_PROVIDERS", "noop")
	_, err := RegistryFromEnv()
	require.ErrorContains(t, err, "noop")
	t.Setenv("AUTH_PROVIDERS", "")
	r, err := RegistryFromEnv()
	require.NoError(t, err)
	require.Empty(t, r.Infos())
	t.Setenv("DM_PRODUCTION", "")
	r, err = RegistryFromEnv()
	require.NoError(t, err)
	require.Len(t, r.Infos(), 1)
	require.Equal(t, NoopName, r.Infos()[0].Name)
}
