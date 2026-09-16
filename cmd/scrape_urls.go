package cmd

import (
	"bufio"
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/extractor"
	"digital-mirror/backend/storage"
	"fmt"
	"os"
	"strings"

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

	stats, err := extractor.ScrapeAll(ctx, inner, urls, scrapeURLsConcurrency, scrapeURLsCacheDir)
	if err != nil {
		return fmt.Errorf("scrape urls: %w", err)
	}
	fmt.Printf("Scraped %d, empty %d, failed %d (total %d). Cache: %s\n", stats.Scraped, stats.Empty, stats.Failed, stats.Total, scrapeURLsCacheDir)
	return nil
}

func collectURLs(ctx context.Context) ([]string, error) {
	if scrapeURLsInput != "" {
		return readURLsFromFile(scrapeURLsInput)
	}
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return nil, fmt.Errorf("create storer: %w", err)
	}
	defer storer.Close()
	return readURLsFromPerson(ctx, storer, scrapeURLsFromPerson)
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

func readURLsFromPerson(ctx context.Context, storer *storage.PostgresStorer, name string) ([]string, error) {
	person, err := core.ResolvePersonID(ctx, storer, name, core.ExactMatchOnly)
	if err != nil {
		return nil, err
	}
	urls, err := storer.GetSourceURLsForPerson(ctx, person.ID)
	if err != nil {
		return nil, fmt.Errorf("read source urls for person %d: %w", person.ID, err)
	}
	return urls, nil
}
