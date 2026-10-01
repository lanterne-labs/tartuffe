package frpolitics

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"digital-mirror/backend/llm"
	"digital-mirror/backend/plugins"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStoreLawsStopsOnUnavailableLLM: an unreachable LLM stops the law loop
// and is returned wrapped (the pipeline's LLM gate sees it); any other
// failure leaves the law for the next run and the loop goes on.
func TestStoreLawsStopsOnUnavailableLLM(t *testing.T) {
	_, store := setupStoreTest(t)
	ctx := context.Background()
	laws := []Law{{Key: "16-7001", Title: "Loi A"}, {Key: "16-7002", Title: "Loi B"}, {Key: "16-7003", Title: "Loi C"}}
	facts := map[string][]FactRecord{}
	for _, l := range laws {
		facts[l.Key] = []FactRecord{{PersonName: "Jean Dupont", LawKey: l.Key}}
	}
	processed := func() map[string]bool {
		keys := []string{"16-7001", "16-7002", "16-7003"}
		settled, err := store.SettledLawKeys(ctx, keys, nil)
		require.NoError(t, err)
		return settled
	}

	t.Run("cut by the deadline", func(t *testing.T) {
		var called []string
		expired, cancel := context.WithDeadline(ctx, time.Now().Add(-time.Second))
		defer cancel()
		walked, err := storeLaws(expired, store, laws, facts, func(_ context.Context, recs []FactRecord) error {
			called = append(called, recs[0].LawKey)
			return nil
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Zero(t, walked)
		assert.Empty(t, called, "no law starts after the deadline")

		cutCtx, cut := context.WithCancel(ctx)
		defer cut()
		walked, err = storeLaws(cutCtx, store, laws, facts, func(ctx context.Context, recs []FactRecord) error {
			called = append(called, recs[0].LawKey)
			if recs[0].LawKey == "16-7002" {
				cut()
				return fmt.Errorf("failed to insert facts: %w", ctx.Err())
			}
			return nil
		})
		require.ErrorIs(t, err, context.Canceled)
		assert.Equal(t, 1, walked, "the cut law is not counted")
		assert.Equal(t, []string{"16-7001", "16-7002"}, called, "no law after the cut")
		assert.Equal(t, map[string]bool{"16-7001": true}, processed(), "the cut law is not marked")
	})

	t.Run("unavailable", func(t *testing.T) {
		var called []string
		walked, err := storeLaws(ctx, store, laws, facts, func(_ context.Context, recs []FactRecord) error {
			called = append(called, recs[0].LawKey)
			if recs[0].LawKey == "16-7002" {
				return fmt.Errorf("failed to insert facts: %w", llm.ErrUnavailable)
			}
			return nil
		})
		require.ErrorIs(t, err, llm.ErrUnavailable)
		assert.Equal(t, 1, walked, "the interrupted law is not counted")
		assert.Equal(t, []string{"16-7001", "16-7002"}, called, "no law after the outage")
		assert.Equal(t, map[string]bool{"16-7001": true}, processed())
	})

	t.Run("other failure", func(t *testing.T) {
		var called []string
		walked, err := storeLaws(ctx, store, laws, facts, func(_ context.Context, recs []FactRecord) error {
			called = append(called, recs[0].LawKey)
			if recs[0].LawKey == "16-7002" {
				return errors.New("bad record")
			}
			return nil
		})
		require.Error(t, err)
		assert.Equal(t, 3, walked)
		assert.NotErrorIs(t, err, llm.ErrUnavailable)
		assert.Equal(t, []string{"16-7001", "16-7002", "16-7003"}, called)
		assert.Equal(t, map[string]bool{"16-7001": true, "16-7003": true}, processed())
	})
}

// TestIngestSkipsDebatsWhenVotesHitUnavailableLLM: votes stopped by an
// unreachable LLM skip the transcripts (they need the LLM too); votes failing
// for another reason do not. Either way the laws the votes went through are
// reported as spent.
func TestIngestSkipsDebatsWhenVotesHitUnavailableLLM(t *testing.T) {
	var debatsRan bool
	debats := func() (DebatsStats, error) { debatsRan = true; return DebatsStats{}, nil }

	stats, err := ingestVotesThenDebats(func() (int, error) { return 4, fmt.Errorf("law 16-1: %w", llm.ErrUnavailable) }, debats)
	require.ErrorIs(t, err, llm.ErrUnavailable)
	assert.False(t, debatsRan)
	assert.Equal(t, 4, stats.Spent)

	stats, err = ingestVotesThenDebats(func() (int, error) { return 0, errors.New("download failed") }, debats)
	require.Error(t, err)
	assert.True(t, debatsRan)
	assert.Zero(t, stats.Spent)
}

// TestLawsLeft: a retry gets the run's law cap minus the laws earlier calls
// went through.
func TestLawsLeft(t *testing.T) {
	assert.Equal(t, dailyMaxLaws, lawsLeft(plugins.IngestOptions{}))
	assert.Equal(t, dailyMaxLaws-12, lawsLeft(plugins.IngestOptions{Spent: 12}))
	assert.Equal(t, 0, lawsLeft(plugins.IngestOptions{MaxItems: 1, Spent: 1}))
}
