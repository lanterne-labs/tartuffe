package storage

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHistoryCursor(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()
	_, err := storer.pool.Exec(ctx, `DELETE FROM history_cursors`)
	require.NoError(t, err)

	_, ok, err := storer.GetHistoryCursor(ctx, "news")
	require.NoError(t, err)
	require.False(t, ok, "a history never read has no cursor")

	first := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, storer.SetHistoryCursor(ctx, "news", first))
	require.NoError(t, storer.SetHistoryCursor(ctx, "news", first.AddDate(0, 0, -30)))
	reached, ok, err := storer.GetHistoryCursor(ctx, "news")
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, reached.Equal(first.AddDate(0, 0, -30)), "reached = %v", reached)

	_, ok, err = storer.GetHistoryCursor(ctx, "twitter")
	require.NoError(t, err)
	require.False(t, ok, "cursors are per plugin")
}
