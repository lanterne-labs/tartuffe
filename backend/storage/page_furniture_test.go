package storage

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A quote stored from a page dated weeks apart is furniture; the same quote
// within the gap, another quote, another person, or the same URL is not.
func TestQuoteOnPageOfOtherDate(t *testing.T) {
	storer, teardown := setupTestDB(t)
	defer teardown()
	ctx := context.Background()

	pid, _, err := storer.AddPerson(ctx, "furniture person")
	require.NoError(t, err)
	other, _, err := storer.AddPerson(ctx, "other furniture person")
	require.NoError(t, err)
	const ticker = "Pillages : il demande aux plateformes de revente en ligne de coopérer"
	id := insertQuotedFact(ctx, t, storer, pid, "fait", ticker, "news", "2021-12-05", makeUnitEmbedding(0))
	url := fmt.Sprintf("https://www.q.test/%d", id)

	at := func(day string) *time.Time {
		d, err := time.Parse("2006-01-02", day)
		require.NoError(t, err)
		return &d
	}
	gap := 3 * 24 * time.Hour
	for _, tc := range []struct {
		name   string
		person int64
		quote  string
		url    string
		day    *time.Time
		want   bool
	}{
		{"same quote, page of 2026", pid, ticker, "https://other.test/a", at("2026-10-20"), true},
		{"same quote, undated page", pid, ticker, "https://other.test/a", nil, true},
		{"same quote, within the gap", pid, ticker, "https://other.test/a", at("2021-12-07"), false},
		{"same URL", pid, ticker, url, at("2026-10-20"), false},
		{"another quote", pid, "une autre phrase", "https://other.test/a", at("2026-10-20"), false},
		{"another person", other, ticker, "https://other.test/a", at("2026-10-20"), false},
	} {
		got, err := storer.QuoteOnPageOfOtherDate(ctx, tc.person, tc.quote, tc.url, tc.day, gap)
		require.NoError(t, err, tc.name)
		require.Equal(t, tc.want, got, tc.name)
	}
}
