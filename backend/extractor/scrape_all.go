package extractor

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
)

// ScrapeStats counts the outcome of one ScrapeAll run.
type ScrapeStats struct {
	// Scraped is the number of URLs that yielded non-empty content.
	Scraped int64
	// Empty is the number of URLs that yielded no content.
	Empty int64
	// Failed is the number of URLs whose extraction errored.
	Failed int64
	// Total is len(urls).
	Total int
}

// ScrapeAll scrapes every URL through ex with concurrency workers, persisting
// each successful result into cacheDir (read-through: already-cached URLs are
// not re-fetched) in the JSON format `dm ingest files` reads. Per-URL failures
// are logged and counted, never fatal. Feeding stops when ctx is cancelled.
func ScrapeAll(ctx context.Context, ex ContentExtractor, urls []string, concurrency int, cacheDir string) (ScrapeStats, error) {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return ScrapeStats{}, fmt.Errorf("create cache dir: %w", err)
	}
	if concurrency < 1 {
		concurrency = 1
	}
	cache := NewCacheExtractor(ex, cacheDir, true, true)

	var (
		ok      int64
		skipped int64
		failed  int64
		wg      sync.WaitGroup
		work    = make(chan string, concurrency*2)
	)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for u := range work {
				content, err := cache.ExtractContent(u)
				if err != nil {
					atomic.AddInt64(&failed, 1)
					log.Warn().Err(err).Str("url", u).Msg("Scrape failed")
					continue
				}
				if content == nil || strings.TrimSpace(content.Content) == "" {
					atomic.AddInt64(&skipped, 1)
					log.Warn().Str("url", u).Msg("Empty content, skipped")
					continue
				}
				atomic.AddInt64(&ok, 1)
				log.Info().Str("url", u).Int("chars", len(content.Content)).Msg("Scraped")
			}
		}()
	}
feed:
	for _, u := range urls {
		// Checked before the select: with a cancelled ctx and a free buffer slot
		// both cases are ready and select would pick one at random.
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			break feed
		case work <- u:
		}
	}
	close(work)
	wg.Wait()

	stats := ScrapeStats{Scraped: ok, Empty: skipped, Failed: failed, Total: len(urls)}
	log.Info().
		Int64("scraped", stats.Scraped).
		Int64("empty_skipped", stats.Empty).
		Int64("failed", stats.Failed).
		Int("total", stats.Total).
		Str("cache_dir", cacheDir).
		Msg("Scrape complete")
	return stats, ctx.Err()
}
