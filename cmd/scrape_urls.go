package cmd

import (
	"bufio"
	"context"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	scrapeURLsInput       string
	scrapeURLsCacheDir    string
	scrapeURLsExtractor   string
	scrapeURLsConcurrency int
	scrapeURLsFromPerson  string
)

var scrapeURLsCmd = &cobra.Command{
	Use:   "urls",
	Short: "Scrape a list of URLs into a local content cache.",
	Long: `Reads URLs and scrapes each one through the configured extractor,
saving the result to a cache directory in the same JSON format used by
'dm ingest files' and 'dm plugin news search --cache-only'.

Two URL sources are supported:
  --input PATH         file with one URL per line (blanks/# comments ignored)
  --from-person NAME   pull DISTINCT source_url values from fact_sources for that person

Examples:
  # Snapshot Macron's existing fact_sources URLs into a cache, then re-process
  dm scrape urls --from-person "Emmanuel Macron" --cache-dir /tmp/macron_cache
  dm facts delete --person "Emmanuel Macron"
  dm ingest files --dir /tmp/macron_cache --target "Emmanuel Macron"

  # From a text file
  dm scrape urls --input urls.txt --cache-dir /tmp/cache`,
	Annotations: map[string]string{"needs_db": "true"}, // needed when --from-person is set
	RunE:        runScrapeURLs,
}

func init() {
	scrapeCmd.AddCommand(scrapeURLsCmd)
	scrapeURLsCmd.Flags().StringVar(&scrapeURLsInput, "input", "", "Path to a text file of URLs (one per line)")
	scrapeURLsCmd.Flags().StringVar(&scrapeURLsFromPerson, "from-person", "", "Pull URLs from fact_sources for this person (alternative to --input)")
	scrapeURLsCmd.Flags().StringVar(&scrapeURLsCacheDir, "cache-dir", "", "Directory to write the cached JSON files (required)")
	scrapeURLsCmd.Flags().StringVar(&scrapeURLsExtractor, "extractor", "auto", "Content extractor (auto, local, webcrawler, firecrawl, apify)")
	scrapeURLsCmd.Flags().IntVar(&scrapeURLsConcurrency, "concurrency", 4, "Parallel scrape workers")
}

func runScrapeURLs(cmd *cobra.Command, args []string) error {
	if scrapeURLsCacheDir == "" {
		return fmt.Errorf("--cache-dir is required")
	}
	if scrapeURLsInput == "" && scrapeURLsFromPerson == "" {
		return fmt.Errorf("either --input or --from-person is required")
	}
	if scrapeURLsInput != "" && scrapeURLsFromPerson != "" {
		return fmt.Errorf("use --input OR --from-person, not both")
	}

	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env not found; relying on environment")
	}

	if err := os.MkdirAll(scrapeURLsCacheDir, 0o755); err != nil {
		return fmt.Errorf("create cache dir: %w", err)
	}

	urls, err := collectURLs(ctx)
	if err != nil {
		return err
	}
	if len(urls) == 0 {
		log.Warn().Msg("No URLs to scrape")
		return nil
	}
	log.Info().Int("count", len(urls)).Str("cache_dir", scrapeURLsCacheDir).Msg("Starting URL scrape")

	inner, err := extractor.NewExtractorFromType(scrapeURLsExtractor)
	if err != nil {
		return fmt.Errorf("create extractor: %w", err)
	}
	// Wrap with CacheExtractor: read=true so already-scraped URLs are skipped,
	// write=true so successful scrapes are persisted for downstream `dm ingest files`.
	cache := extractor.NewCacheExtractor(inner, scrapeURLsCacheDir, true, true)

	var (
		ok      int64
		skipped int64
		failed  int64
		wg      sync.WaitGroup
		work    = make(chan string, scrapeURLsConcurrency*2)
	)

	for i := 0; i < scrapeURLsConcurrency; i++ {
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
	for _, u := range urls {
		work <- u
	}
	close(work)
	wg.Wait()

	log.Info().
		Int64("scraped", ok).
		Int64("empty_skipped", skipped).
		Int64("failed", failed).
		Int("total", len(urls)).
		Str("cache_dir", scrapeURLsCacheDir).
		Msg("Scrape complete")
	fmt.Printf("Scraped %d, empty %d, failed %d (total %d). Cache: %s\n", ok, skipped, failed, len(urls), scrapeURLsCacheDir)
	return nil
}

func collectURLs(ctx context.Context) ([]string, error) {
	if scrapeURLsInput != "" {
		return readURLsFromFile(scrapeURLsInput)
	}
	return readURLsFromPerson(ctx, scrapeURLsFromPerson)
}

func readURLsFromFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	var urls []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		urls = append(urls, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return urls, nil
}

func readURLsFromPerson(ctx context.Context, name string) ([]string, error) {
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return nil, fmt.Errorf("create storer: %w", err)
	}
	defer storer.Close()
	personID, err := storer.GetPersonIDByName(ctx, util.NormalizeString(name))
	if err != nil {
		return nil, fmt.Errorf("find person %q: %w", name, err)
	}
	urls, err := storer.GetSourceURLsForPerson(ctx, personID)
	if err != nil {
		return nil, fmt.Errorf("read source urls for person %d: %w", personID, err)
	}
	return urls, nil
}
