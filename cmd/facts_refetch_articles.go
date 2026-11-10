package cmd

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/extractor"
)

// refetchMinChars is the least text a page read again must give to be kept:
// under it the page is a paywall stub, a cookie wall or an error page.
const refetchMinChars = 600

// refetchWorkers is how many pages are read at once. The pace of each site
// is webfetch's (one request a second and two at once per domain): the
// workers only spread the run over the sites.
const refetchWorkers = 8

// factsRefetchArticlesCmd reads again the news pages whose text was not
// kept, and stores it (source_documents.content). The pages extracted before
// 2026-10-30 lost their text at extraction; without it the date finder
// (dm facts statement-dates) and the explanation writer read a fact and its
// quote alone, and a statement that only the article dates ("lançait-il en
// 2017") keeps the date of the article. No fact is extracted or changed.
var factsRefetchArticlesCmd = &cobra.Command{
	Use:         "refetch-articles",
	Short:       "Read again the news pages whose text was not kept, and store it (the candidates' facts by default)",
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runFactsRefetchArticles,
}

func init() {
	factsCmd.AddCommand(factsRefetchArticlesCmd)
	factsRefetchArticlesCmd.Flags().Bool("apply", false, "store the pages (default: read 10 and report)")
	factsRefetchArticlesCmd.Flags().Bool("all", false, "the pages of every person's facts, not only the candidates'")
	factsRefetchArticlesCmd.Flags().Int("limit", 0, "at most this many pages (0: all; a dry run reads 10)")
}

func runFactsRefetchArticles(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	apply, _ := cmd.Flags().GetBool("apply")
	all, _ := cmd.Flags().GetBool("all")
	limit, _ := cmd.Flags().GetInt("limit")
	if !apply && limit == 0 {
		limit = 10
	}
	if limit == 0 {
		limit = 1 << 30
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()
	reader, err := extractor.NewExtractorFromType("local")
	if err != nil {
		return fmt.Errorf("page reader: %w", err)
	}
	urls, err := storer.ListNewsURLsWithoutText(ctx, !all, limit)
	if err != nil {
		return err
	}

	var mu sync.Mutex
	var stored, thin, failed int
	work := make(chan string)
	var wg sync.WaitGroup
	for i := 0; i < refetchWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for u := range work {
				page, err := reader.ExtractContent(u)
				reason := ""
				switch {
				case err != nil:
					reason = err.Error()
				case len([]rune(strings.TrimSpace(page.Content))) < refetchMinChars:
					reason = fmt.Sprintf("too thin: %d characters", len([]rune(strings.TrimSpace(page.Content))))
				}
				mu.Lock()
				switch {
				case reason == "":
					stored++
				case err != nil:
					failed++
				default:
					thin++
				}
				n := stored + thin + failed
				mu.Unlock()
				if n%100 == 0 {
					log.Info().Int("read", n).Int("of", len(urls)).Msg("refetch-articles")
				}
				if reason != "" {
					log.Warn().Str("url", u).Str("reason", reason).Msg("refetch-articles: the page gave no text")
					if apply {
						if err := storer.MarkRefetchFailed(ctx, u, reason); err != nil {
							log.Error().Err(err).Str("url", u).Msg("refetch-articles: marking failed")
						}
					}
					continue
				}
				if !apply {
					fmt.Printf("%s\t%d characters\t%s\n", u, len([]rune(page.Content)), page.Title)
					continue
				}
				if err := storer.StoreRefetchedArticle(ctx, u, page.Title, strings.TrimSpace(page.Content)); err != nil {
					log.Error().Err(err).Str("url", u).Msg("refetch-articles: storing failed")
				}
			}
		}()
	}
	for _, u := range urls {
		work <- u
	}
	close(work)
	wg.Wait()

	mode := "dry run, nothing stored"
	if apply {
		mode = "stored"
	}
	fmt.Printf("%d pages without text: %d read, %d too thin, %d failed (%s)\n", len(urls), stored, thin, failed, mode)
	return nil
}
