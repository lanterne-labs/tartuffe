package frpolitics

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"digital-mirror/backend/llm"

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

	t.Run("unavailable", func(t *testing.T) {
		var called []string
		err := storeLaws(ctx, store, laws, facts, func(_ context.Context, recs []FactRecord) error {
			called = append(called, recs[0].LawKey)
			if recs[0].LawKey == "16-7002" {
				return fmt.Errorf("failed to insert facts: %w", llm.ErrUnavailable)
			}
			return nil
		})
		require.ErrorIs(t, err, llm.ErrUnavailable)
		assert.Equal(t, []string{"16-7001", "16-7002"}, called, "no law after the outage")
		assert.Equal(t, map[string]bool{"16-7001": true}, processed())
	})

	t.Run("other failure", func(t *testing.T) {
		var called []string
		err := storeLaws(ctx, store, laws, facts, func(_ context.Context, recs []FactRecord) error {
			called = append(called, recs[0].LawKey)
			if recs[0].LawKey == "16-7002" {
				return errors.New("bad record")
			}
			return nil
		})
		require.Error(t, err)
		assert.NotErrorIs(t, err, llm.ErrUnavailable)
		assert.Equal(t, []string{"16-7001", "16-7002", "16-7003"}, called)
		assert.Equal(t, map[string]bool{"16-7001": true, "16-7003": true}, processed())
	})
}

// TestIngestSkipsDebatsWhenVotesHitUnavailableLLM: votes stopped by an
// unreachable LLM skip the transcripts (they need the LLM too); votes failing
// for another reason do not.
func TestIngestSkipsDebatsWhenVotesHitUnavailableLLM(t *testing.T) {
	var debatsRan bool
	debats := func() (DebatsStats, error) { debatsRan = true; return DebatsStats{}, nil }

	_, err := ingestVotesThenDebats(func() error { return fmt.Errorf("law 16-1: %w", llm.ErrUnavailable) }, debats)
	require.ErrorIs(t, err, llm.ErrUnavailable)
	assert.False(t, debatsRan)

	_, err = ingestVotesThenDebats(func() error { return errors.New("download failed") }, debats)
	require.Error(t, err)
	assert.True(t, debatsRan)
}
