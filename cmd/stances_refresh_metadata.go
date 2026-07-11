package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"
	"strings"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	refreshMetadataPerson    string
	refreshMetadataNonFrOnly bool
)

// looksFrench reports whether a stance summary already reads as French, used by
// --only-non-french to skip already-French stances (both a resume mechanism and a
// guard against needlessly regenerating good French summaries). Conservative: it
// leans toward "not French" on borderline text so English stances are not skipped.
func looksFrench(summary string) bool {
	s := strings.ToLower(summary)
	if strings.ContainsAny(s, "éèàçêâîôûùïüœ") {
		return true
	}
	for _, m := range []string{" le ", " la ", " les ", " des ", " une ", " du ", " au ", " qu'", " d'", " l'", " a déclaré", " a affirmé", " a annoncé"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

var stancesRefreshMetadataCmd = &cobra.Command{
	Use:   "refresh-metadata",
	Short: "Regenerate stance label + summary (native language, now French) without re-clustering.",
	Long: `Re-runs the metadata LLM for existing stances: it regenerates each stance's
label and summary from its current facts, in French, WITHOUT changing fact-to-stance
assignments (unlike 'dm stances backfill'). Translations (_en, etc.) are left to the
translation phase — run 'dm translate run' afterwards to fill them.

Used to make existing stances' native language consistent (they used to follow the
facts' language, so some were English).`,
	RunE: runStancesRefreshMetadata,
}

func init() {
	stancesCmd.AddCommand(stancesRefreshMetadataCmd)
	stancesRefreshMetadataCmd.Flags().StringVar(&refreshMetadataPerson, "person", "", "Only refresh this person's stances (optional)")
	stancesRefreshMetadataCmd.Flags().BoolVar(&refreshMetadataNonFrOnly, "only-non-french", false, "Skip stances whose summary already looks French (resume / avoid re-doing good ones)")
}

func runStancesRefreshMetadata(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	adapter := storage.NewStanceStorerAdapter(storer)
	stanceManager, err := core.NewStanceManagerFromEnv(adapter)
	if err != nil {
		return fmt.Errorf("failed to create stance manager: %w", err)
	}

	var persons []storage.PersonInfo
	if refreshMetadataPerson != "" {
		id, err := storer.GetPersonIDByName(ctx, util.NormalizeString(refreshMetadataPerson))
		if err != nil {
			return fmt.Errorf("find person %q: %w", refreshMetadataPerson, err)
		}
		persons = []storage.PersonInfo{{ID: id, Name: util.NormalizeString(refreshMetadataPerson)}}
	} else {
		persons, err = storer.GetAllPersons(ctx)
		if err != nil {
			return fmt.Errorf("list persons: %w", err)
		}
	}

	var refreshed, failed, skipped int
	for _, p := range persons {
		docs, err := storer.GetStanceDocumentsForPerson(ctx, p.ID)
		if err != nil {
			log.Warn().Err(err).Str("person", p.Name).Msg("failed to load stances; skipping")
			continue
		}
		for _, d := range docs {
			if refreshMetadataNonFrOnly && looksFrench(d.Summary) {
				skipped++
				continue
			}
			if err := stanceManager.RefreshStanceMetadata(ctx, d.ID); err != nil {
				log.Warn().Err(err).Int64("stance_id", d.ID).Str("person", p.Name).Msg("refresh failed")
				failed++
				continue
			}
			refreshed++
		}
	}
	fmt.Printf("Stance metadata refresh: %d refreshed, %d skipped (already French), %d failed. Run 'dm translate run' to fill translations.\n", refreshed, skipped, failed)
	return nil
}
