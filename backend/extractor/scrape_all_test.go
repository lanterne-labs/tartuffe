package extractor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedExtractor returns a canned result per URL and counts calls.
type scriptedExtractor struct {
	mu    sync.Mutex
	calls map[string]int
	body  map[string]string
	fail  map[string]bool
}

func (s *scriptedExtractor) ExtractContent(url string) (*ExtractedContent, error) {
	s.mu.Lock()
	s.calls[url]++
	s.mu.Unlock()
	if s.fail[url] {
		return nil, errors.New("fetch failed")
	}
	return &ExtractedContent{URL: url, Title: "t", Content: s.body[url]}, nil
}

func TestScrapeAll_CountsAndCaches(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	ex := &scriptedExtractor{
		calls: map[string]int{},
		body:  map[string]string{"https://a": "hello world", "https://b": "   "},
		fail:  map[string]bool{"https://c": true},
	}
	urls := []string{"https://a", "https://b", "https://c"}

	stats, err := ScrapeAll(context.Background(), ex, urls, 2, dir)
	require.NoError(t, err)
	assert.Equal(t, ScrapeStats{Scraped: 1, Empty: 1, Failed: 1, Total: 3}, stats)

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	// Only successful extractions are written; the empty one is cached too since
	// the extractor returned content (it is the scrape loop that counts it empty).
	assert.Len(t, entries, 2)

	// Re-running reads from the cache: the successful URLs are not fetched again.
	stats, err = ScrapeAll(context.Background(), ex, urls, 2, dir)
	require.NoError(t, err)
	assert.Equal(t, ScrapeStats{Scraped: 1, Empty: 1, Failed: 1, Total: 3}, stats)
	assert.Equal(t, 1, ex.calls["https://a"])
	assert.Equal(t, 1, ex.calls["https://b"])
	assert.Equal(t, 2, ex.calls["https://c"], "failures are retried")
}

func TestScrapeAll_CancelledContextStopsFeeding(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ex := &scriptedExtractor{calls: map[string]int{}, body: map[string]string{"https://a": "x"}}
	stats, err := ScrapeAll(ctx, ex, []string{"https://a"}, 0, t.TempDir())
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, stats.Total)
	assert.Equal(t, int64(0), stats.Scraped+stats.Empty+stats.Failed)
}
