package cmd

import (
	"context"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"

	"digital-mirror/backend/deps"
	"digital-mirror/backend/search"
)

// factsResolveRedirectsCmd moves the news fact sources stored under a Google
// redirect link (google.com/goto, google.com/url: the search APIs return
// them for the results past the first ones since 2026-10-30) to the article
// the redirect points at. The pages were read through the redirect, so the
// facts are right; only the source URL was the redirect's. The search client
// now resolves the links before they are stored (search.ResolveGoogleLink).
var factsResolveRedirectsCmd = &cobra.Command{
	Use:   "resolve-redirects",
	Short: "Replace Google redirect links of news fact sources by the article's URL",
	RunE:  runFactsResolveRedirects,
}

func init() {
	factsCmd.AddCommand(factsResolveRedirectsCmd)
	factsResolveRedirectsCmd.Flags().Bool("apply", false, "write the resolved URLs (default: report only)")
}

func runFactsResolveRedirects(cmd *cobra.Command, args []string) error {
	_ = godotenv.Load()
	ctx := context.Background()
	apply, _ := cmd.Flags().GetBool("apply")

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	defer storer.Close()

	var afterID int64
	var seen, resolved, failed, written int
	for {
		rows, err := storer.ListFactSourcesByType(ctx, "news", afterID, 1000)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			afterID = r.ID
			if !search.IsGoogleRedirect(r.SourceURL) {
				continue
			}
			seen++
			final, err := search.ResolveGoogleLink(ctx, r.SourceURL)
			if err != nil {
				failed++
				log.Warn().Err(err).Int64("fact_source_id", r.ID).Msg("redirect not resolved")
				continue
			}
			resolved++
			if !apply {
				continue
			}
			if err := storer.SetFactSourceURLAndDate(ctx, r.ID, final, r.PublishedAt); err != nil {
				return err
			}
			written++
		}
	}
	fmt.Printf("news fact sources under a Google redirect: %d; resolved %d, not resolved %d, written %d\n", seen, resolved, failed, written)
	if !apply {
		fmt.Println("dry run: nothing written (use --apply)")
	}
	return nil
}
