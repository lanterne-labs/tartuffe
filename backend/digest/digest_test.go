package digest

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStore struct {
	counts  Counts
	since   time.Time
	jobs    map[string]time.Time
	last    time.Time
	ballots int
	sent    bool
}

func (f *fakeStore) DigestCounts(_ context.Context, since time.Time) (Counts, error) {
	f.since = since
	return f.counts, nil
}
func (f *fakeStore) LastJobAt(_ context.Context, job string) (time.Time, bool, error) {
	at, ok := f.jobs[job]
	return at, ok, nil
}
func (f *fakeStore) LastDigest(context.Context) (time.Time, int, bool, error) {
	return f.last, f.ballots, f.sent, nil
}
func (f *fakeStore) RecordDigest(_ context.Context, _ Counts, ballots int) error {
	f.sent, f.ballots = true, ballots
	return nil
}

// TestDigest: one message a day at its time, four lines; the vote line gives
// the ballots and those since the last digest; the last line says what
// needs a person, or that nothing does.
func TestDigest(t *testing.T) {
	zone, _ := time.LoadLocation("Europe/Paris")
	now := time.Date(2026, 11, 14, 7, 59, 0, 0, zone)
	store := &fakeStore{counts: Counts{Facts: 1240, Contradictions: 3},
		jobs: map[string]time.Time{"maintenance": now.Add(-20 * time.Hour), "feed": now.Add(-2 * time.Hour)}}
	var sent []string
	ballots := 14
	errs := &ErrorCounter{}
	d := &Digest{Store: store, Hour: 8, Zone: zone, MaintenanceEvery: 24 * time.Hour, Errors: errs,
		Send:    func(_ context.Context, m string) error { sent = append(sent, m); return nil },
		Ballots: func(context.Context) (int, bool, error) { return ballots, true, nil },
		Now:     func() time.Time { return now }}
	ctx := context.Background()

	require.NoError(t, d.Tick(ctx))
	assert.Empty(t, sent, "not before its time")
	now = now.Add(2 * time.Minute)
	require.NoError(t, d.Tick(ctx))
	require.Len(t, sent, 1)
	assert.Equal(t, "📋 <b>Bilan du 14/11</b>\nFaits : +1240\nContradictions : +3\nVote : 14 bulletins (+0)\n✅ Rien d'urgent", sent[0])
	assert.Equal(t, now.Add(-24*time.Hour), store.since, "the first digest covers a day")
	store.last = now
	require.NoError(t, d.Tick(ctx))
	assert.Len(t, sent, 1, "once a day")

	// The next day: two more ballots, and things to look at.
	now = now.Add(24 * time.Hour)
	ballots = 16
	store.counts = Counts{Facts: 10, FailedTasks: 2, FailedPosts: 1}
	store.jobs["maintenance"] = now.Add(-50 * time.Hour)
	store.jobs["feed"] = now.Add(-26 * time.Hour)
	logger := zerolog.New(io.Discard).Hook(errs)
	for i := 0; i < ErrorsWorthALook; i++ {
		logger.Error().Msg("x")
	}
	logger.Warn().Msg("not an error")
	require.NoError(t, d.Tick(ctx))
	require.Len(t, sent, 2)
	assert.Contains(t, sent[1], "Vote : 16 bulletins (+2)\n")
	assert.Contains(t, sent[1], "⚠️ À regarder : maintenance bloquée depuis 50 h ; le flux du jour n'a pas tourné ; 2 collecte(s) en échec ; 1 tweet(s) non publié(s) ; 50 erreurs dans les logs")
	assert.Equal(t, store.last, store.since, "it covers the time since the last digest")
	assert.Equal(t, 0, errs.Count(), "the count starts again")
}
