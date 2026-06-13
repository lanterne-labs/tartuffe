package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var personsUnaccentApply bool

// personsUnaccentCmd merges accent-duplicate person rows and rewrites all person
// names to their accent-free canonical form, so "éric ciotti" and "eric ciotti"
// become a single "eric ciotti" row. Dry-run by default.
var personsUnaccentCmd = &cobra.Command{
	Use:   "unaccent",
	Short: "Merge accent-duplicate persons and strip accents from all person names.",
	Long: `Finds person rows whose names collide after accent/case folding (the same
person stored twice, e.g. "éric ciotti" + "eric ciotti"), merges each group into a
single survivor (curated/most-facts wins, via ChooseCanonical), then rewrites every
person name to its accent-free form so accent variants can never split a person again.

Dry-run by default — prints what WOULD merge. Pass --apply to mutate.`,
	Args:        cobra.NoArgs,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runPersonsUnaccent,
}

func init() {
	personsCmd.AddCommand(personsUnaccentCmd)
	personsUnaccentCmd.Flags().BoolVar(&personsUnaccentApply, "apply", false, "Actually merge dups and strip accents. Default is dry-run.")
}

func runPersonsUnaccent(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env not found, relying on environment")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	groups, err := storer.GetAccentDuplicateGroups(ctx)
	if err != nil {
		return fmt.Errorf("failed to load accent-dup groups: %w", err)
	}

	if !personsUnaccentApply {
		fmt.Println("DRY RUN — pass --apply to merge + strip. Accent-duplicate groups:")
	}

	var merged, skipped int
	for _, g := range groups {
		survivor, ok := storage.ChooseCanonical(g)
		if !ok {
			skipped++
			log.Warn().Str("name", g[0].Name).Int("members", len(g)).
				Msg("Skipping accent-dup group (2+ curated members — won't merge real entities)")
			continue
		}
		var fromIDs []int64
		for _, m := range g {
			if m.ID != survivor.ID {
				fromIDs = append(fromIDs, m.ID)
			}
		}
		fmt.Printf("  %-45s survivor=#%d (facts=%d) <- %v\n", survivor.Name, survivor.ID, survivor.FactCount, fromIDs)
		if !personsUnaccentApply {
			continue
		}
		for _, from := range fromIDs {
			if err := storer.MergePersons(ctx, from, survivor.ID); err != nil {
				return fmt.Errorf("merge %d into %d (%q): %w", from, survivor.ID, survivor.Name, err)
			}
			merged++
		}
	}

	if personsUnaccentApply {
		stripped, err := storer.StripAllPersonAccents(ctx)
		if err != nil {
			return fmt.Errorf("strip accents: %w", err)
		}
		log.Info().Int("groups", len(groups)).Int("rows_merged_away", merged).Int("groups_skipped", skipped).
			Int64("names_stripped", stripped).Msg("Unaccent complete")
		fmt.Printf("\nDone: %d groups, %d rows merged away, %d groups skipped, %d names stripped of accents.\n",
			len(groups), merged, skipped, stripped)
	} else {
		fmt.Printf("\n%d accent-dup groups (%d would be skipped as 2+ curated). Re-run with --apply.\n", len(groups), skipped)
	}
	return nil
}
