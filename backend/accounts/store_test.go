package accounts_test

import (
	"context"
	"os"
	"testing"
	"time"

	"digital-mirror/backend/accounts"
	"digital-mirror/backend/storage"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func openStore(t *testing.T) *accounts.PGStore {
	t.Helper()
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("DATABASE_URL not set, skipping database tests")
	}
	ctx := context.Background()
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	require.NoError(t, err)
	t.Cleanup(storer.Close)
	_, err = storer.Pool().Exec(ctx, "TRUNCATE app_users, auth_challenges RESTART IDENTITY CASCADE")
	require.NoError(t, err)
	return accounts.NewPGStore(storer.Pool(), accounts.Hooks{})
}

// TestStoreSessionsAndChallenges: a user is one identity; a live token finds
// its user, an ended one does not; a challenge serves once, for its provider,
// before it expires.
func TestStoreSessionsAndChallenges(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	u, err := s.UpsertUser(ctx, "noop", "one", "One")
	require.NoError(t, err)
	again, err := s.UpsertUser(ctx, "noop", "one", "One")
	require.NoError(t, err)
	assert.Equal(t, u.ID, again.ID)
	assert.False(t, u.Verified)

	hash := []byte("0123456789abcdef0123456789abcdef")
	require.NoError(t, s.CreateSession(ctx, u.ID, hash, time.Now().Add(time.Hour)))
	su, err := s.SessionUser(ctx, hash)
	require.NoError(t, err)
	require.NotNil(t, su)
	assert.Equal(t, u.ID, su.ID)
	require.NoError(t, s.DeleteSession(ctx, hash))
	su, err = s.SessionUser(ctx, hash)
	require.NoError(t, err)
	assert.Nil(t, su)

	require.NoError(t, s.CreateChallenge(ctx, "zkpassport", "c1", time.Now().Add(time.Minute)))
	require.NoError(t, s.CreateChallenge(ctx, "zkpassport", "old", time.Now().Add(-time.Minute)))
	ok, err := s.ConsumeChallenge(ctx, "noop", "c1")
	require.NoError(t, err)
	assert.False(t, ok, "another provider's challenge")
	ok, err = s.ConsumeChallenge(ctx, "zkpassport", "c1")
	require.NoError(t, err)
	assert.True(t, ok)
	ok, err = s.ConsumeChallenge(ctx, "zkpassport", "c1")
	require.NoError(t, err)
	assert.False(t, ok, "used once already")
	ok, err = s.ConsumeChallenge(ctx, "zkpassport", "old")
	require.NoError(t, err)
	assert.False(t, ok, "expired")
}

// TestStoreMoves: a pending move serves once, for its user, before it
// expires; the pseudonym is the user's.
func TestStoreMoves(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a, err := s.UpsertUser(ctx, "google", "a", "")
	require.NoError(t, err)
	b, err := s.UpsertUser(ctx, "google", "b", "")
	require.NoError(t, err)
	require.NoError(t, s.CreateMove(ctx, a.ID, "zkpassport:doc", []byte("tok-live"), time.Now().Add(time.Minute)))
	require.NoError(t, s.CreateMove(ctx, a.ID, "zkpassport:doc", []byte("tok-old"), time.Now().Add(-time.Minute)))
	_, ok, err := s.ConsumeMove(ctx, b.ID, []byte("tok-live"))
	require.NoError(t, err)
	assert.False(t, ok, "another user's move")
	sub, ok, err := s.ConsumeMove(ctx, a.ID, []byte("tok-live"))
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "zkpassport:doc", sub)
	_, ok, err = s.ConsumeMove(ctx, a.ID, []byte("tok-live"))
	require.NoError(t, err)
	assert.False(t, ok, "used once already")
	_, ok, err = s.ConsumeMove(ctx, a.ID, []byte("tok-old"))
	require.NoError(t, err)
	assert.False(t, ok, "expired")

	require.NoError(t, s.SetDisplayName(ctx, a.ID, "Le Citoyen"))
	again, err := s.UpsertUser(ctx, "google", "a", "")
	require.NoError(t, err)
	assert.Equal(t, "Le Citoyen", again.DisplayName, "a sign-in keeps the chosen pseudonym")
}

// TestStoreVerification: an account keeps its document; the document moves
// to the account that shows it last.
func TestStoreVerification(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	a, err := s.UpsertUser(ctx, "google", "a", "")
	require.NoError(t, err)
	b, err := s.UpsertUser(ctx, "google", "b", "")
	require.NoError(t, err)

	from, err := s.AttachVerification(ctx, a.ID, "zkpassport:doc", true)
	require.NoError(t, err)
	assert.Zero(t, from)
	from, err = s.AttachVerification(ctx, a.ID, "zkpassport:doc", true)
	require.NoError(t, err)
	assert.Zero(t, from, "the same document again")
	_, err = s.AttachVerification(ctx, a.ID, "zkpassport:other", true)
	assert.ErrorIs(t, err, accounts.ErrOtherDocument)

	from, err = s.AttachVerification(ctx, b.ID, "zkpassport:doc", false)
	assert.ErrorIs(t, err, accounts.ErrHeldElsewhere, "no move unless asked")
	assert.Equal(t, a.ID, from)
	from, err = s.AttachVerification(ctx, b.ID, "zkpassport:doc", true)
	require.NoError(t, err)
	assert.Equal(t, a.ID, from)
	hash := []byte("abcdef0123456789abcdef0123456789")
	require.NoError(t, s.CreateSession(ctx, a.ID, hash, time.Now().Add(time.Hour)))
	su, err := s.SessionUser(ctx, hash)
	require.NoError(t, err)
	assert.False(t, su.Verified, "the old account lost the identity")
	from, err = s.AttachVerification(ctx, a.ID, "zkpassport:other", true)
	require.NoError(t, err, "and may take another document")
	assert.Zero(t, from)
}
