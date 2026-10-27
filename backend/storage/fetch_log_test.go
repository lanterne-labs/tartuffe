package storage

import (
	"context"
	"testing"
	"time"

	"digital-mirror/backend/webfetch"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFetchLog: the outcomes are written under their source and domain.
func TestFetchLog(t *testing.T) {
	storer, cleanup := setupTestDB(t)
	defer cleanup()
	ctx, cancel := context.WithCancel(context.Background())
	_, err := storer.pool.Exec(ctx, "TRUNCATE fetch_log")
	require.NoError(t, err)

	f := NewFetchLog(ctx, storer)
	f.Record(webfetch.Outcome{At: time.Now(), Source: "news", Host: "www.lemonde.fr", Status: 200, Outcome: "ok", Duration: 120 * time.Millisecond})
	f.Record(webfetch.Outcome{At: time.Now(), Source: "news", Host: "www.lemonde.fr", Outcome: "article_thin", Note: true})
	cancel() // the writer flushes what it holds when it stops

	var rows []string
	require.Eventually(t, func() bool {
		r, err := storer.pool.Query(context.Background(), `SELECT source || ' ' || domain || ' ' || outcome || ' ' || note::text || ' ' || duration_ms FROM fetch_log ORDER BY id`)
		require.NoError(t, err)
		rows = rows[:0]
		for r.Next() {
			var s string
			require.NoError(t, r.Scan(&s))
			rows = append(rows, s)
		}
		return len(rows) == 2
	}, 5*time.Second, 50*time.Millisecond)
	assert.Equal(t, []string{"news lemonde.fr ok false 120", "news lemonde.fr article_thin true 0"}, rows)
}
