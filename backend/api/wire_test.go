package api

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// On the host the API refuses to start without the log key (a key made at
// each deploy would break every pinned checkpoint); locally it makes one.
func TestPollsFromEnvRefusesUnsetKeyOnthe host(t *testing.T) {
	t.Setenv("VOTE_LOG_KEY", "")
	t.Setenv("DM_PRODUCTION", "production")
	_, err := PollsFromEnv()
	require.ErrorContains(t, err, "VOTE_LOG_KEY")
	t.Setenv("DM_PRODUCTION", "")
	cfg, err := PollsFromEnv()
	require.NoError(t, err)
	require.NotNil(t, cfg.Signer)
}
