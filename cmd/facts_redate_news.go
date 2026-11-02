package cmd

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/plugins/news"
	"digital-mirror/backend/search"
	"digital-mirror/backend/storage"
)

// factsRedateNewsCmd dates the news fact sources stored without a date.
// From 2026-10-28 the backfill windows read pages whose own page carried no
// date (a page read on a retry after a bot check: lejdd.fr, leparisien.fr),
// and the search result's date was lost: Serper gives "8 oct. 2025" in
// French, which was not parsed (mirror-rxd). Both are fixed at fetch time;
// this repairs the stored pages: the date in the URL first, then, with
// --search, the date of the search result for the page's title that links
// to the same page.
var factsRedateNewsCmd = &cobra.Command{
	Use:         "redate-news",
	Short:       "Date the undated news fact sources from their URL or the search result",
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runFactsRedateNews,
}

func init() {
	factsCmd.AddCommand(factsRedateNewsCmd)
	factsRedateNewsCmd.Flags().Bool("apply", false, "write the dates (default: report only)")
	factsRedateNewsCmd.Flags().Bool("search", false, "search the page's title for the pages whose URL carries no date (one search per page)")
}

func runFactsRedateNews(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	apply, _ := cmd.Flags().GetBool("apply")
	withSearch, _ := cmd.Flags().GetBool("search")

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	var searcher search.NewsSearcher
	if withSearch {
		if searcher, err = deps.New(storer).NewsSearcher(); err != nil {
			return fmt.Errorf("news searcher: %w", err)
		}
	}

	pages, err := storer.ListUndatedNewsPages(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	var byURL, bySearch, undated, sources int
	var written int64
	for _, p := range pages {
		date, how := news.DateFromURL(p.URL, now), "url"
		if date == nil && searcher != nil && p.Title != "" {
			date, how = searchResultDate(searcher, p), "search"
		}
		if date == nil {
			undated++
			continue
		}
		if how == "url" {
			byURL++
		} else {
			bySearch++
		}
		sources += p.Sources
		log.Debug().Str("url", p.URL).Str("by", how).Time("date", *date).Msg("redate-news")
		if !apply {
			continue
		}
		n, err := storer.SetNewsPageDate(ctx, p.URL, *date)
		if err != nil {
			return err
		}
		written += n
	}
	fmt.Printf("undated news pages: %d; dated by the URL %d, by the search %d (%d fact sources), still undated %d; written %d\n",
		len(pages), byURL, bySearch, sources, undated, written)
	if !apply {
		fmt.Println("dry run: nothing written (use --apply)")
	}
	return nil
}

// searchResultDate searches the page's title and returns the date of the
// result that links to the same page, or nil.
func searchResultDate(searcher search.NewsSearcher, p storage.UndatedNewsPage) *time.Time {
	results, err := searcher.SearchNews(`"`+p.Title+`"`, search.NewsSearchOptions{Limit: 10, Language: "fr"})
	if err != nil {
		log.Warn().Err(err).Str("url", p.URL).Msg("redate-news: search failed")
		return nil
	}
	want := comparableURL(p.URL)
	for _, r := range results {
		if r.Date != nil && comparableURL(r.Link) == want {
			return r.Date
		}
	}
	return nil
}

// comparableURL is a URL without its scheme, www., query, fragment and
// trailing slash: two links to one article compare equal.
func comparableURL(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return strings.TrimSpace(raw)
	}
	return strings.TrimPrefix(strings.ToLower(u.Host), "www.") + strings.TrimSuffix(u.Path, "/")
}
