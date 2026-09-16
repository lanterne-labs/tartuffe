package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	refreshMetadataPerson    string
	refreshMetadataNonFrOnly bool
	refreshMetadataFull      bool
)

var stancesRefreshMetadataCmd = &cobra.Command{
	Use:   "refresh-metadata",
	Short: "Regenerate stance label + summary (native language, now French) without re-clustering.",
	Long: `Re-runs the metadata LLM for existing stances: it regenerates each stance's
label and summary from its current facts, in French, WITHOUT changing fact-to-stance
assignments (unlike 'dm stances backfill'). Translations (_en, etc.) are left to the
translation phase — run 'dm translate run' afterwards to fill them.

Used to make existing stances' native language consistent (they used to follow the
facts' language, so some were English).

By default a stance is brought up to date incrementally: a full rebuild when its
facts fit the model context, otherwise only the facts added since its last
refresh are folded into the current label + summary (no-op when nothing is new).
--full discards the current label + summary and re-derives them from every fact
(rolling fold for stances too large for one call).`,
	RunE: runStancesRefreshMetadata,
}

func init() {
	stancesCmd.AddCommand(stancesRefreshMetadataCmd)
	stancesRefreshMetadataCmd.Flags().StringVar(&refreshMetadataPerson, "person", "", "Only refresh this person's stances (optional)")
	stancesRefreshMetadataCmd.Flags().BoolVar(&refreshMetadataNonFrOnly, "only-non-french", false, "Skip stances whose summary already looks French (resume / avoid re-doing good ones)")
	stancesRefreshMetadataCmd.Flags().BoolVar(&refreshMetadataFull, "full", false, "Rebuild label + summary from every fact, ignoring the current summary")
}

func runStancesRefreshMetadata(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	stanceManager, err := core.NewStanceManagerFromEnv(storer)
	if err != nil {
		return fmt.Errorf("failed to create stance manager: %w", err)
	}

	persons, err := personsForCommand(ctx, storer, refreshMetadataPerson, storer.GetAllPersons)
	if err != nil {
		return err
	}

	stats := stanceManager.RefreshMetadataForPersons(ctx, persons, core.StanceRefreshOptions{
		OnlyNonFrench: refreshMetadataNonFrOnly,
		Full:          refreshMetadataFull,
	})
	fmt.Printf("Stance metadata refresh: %d refreshed, %d skipped (already French), %d failed. Run 'dm translate run' to fill translations.\n", stats.Refreshed, stats.Skipped, stats.Failed)
	return nil
}
