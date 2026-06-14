package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var personsUnaccentApply bool

// personsUnaccentCmd merges person rows that collide on the canonical person-name
// form (util.NormalizePersonName) and rewrites every name to that form, so accent,
// curly-apostrophe, fancy-dash and whitespace variants can never split a person.
var personsUnaccentCmd = &cobra.Command{
	Use:   "unaccent",
	Short: "Merge variant-duplicate persons and rewrite all names to the canonical form.",
	Long: `Finds person rows whose names collide under util.NormalizePersonName (the same
canonical form names are stored under: lowercase, accent-stripped, typographic
punctuation folded to ASCII, whitespace collapsed) — e.g. "éric ciotti" + "eric ciotti",
or "d'azur" + "d'azur". Merges each group into a single survivor (curated/most-facts
wins, via ChooseCanonical), then rewrites every remaining name to its canonical form.

Dry-run by default — prints what WOULD merge/rename. Pass --apply to mutate.`,
	Args:        cobra.NoArgs,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runPersonsUnaccent,
}

func init() {
	personsCmd.AddCommand(personsUnaccentCmd)
	personsUnaccentCmd.Flags().BoolVar(&personsUnaccentApply, "apply", false, "Actually merge dups and canonicalize names. Default is dry-run.")
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

	members, err := storer.GetAllDedupMembers(ctx)
	if err != nil {
		return fmt.Errorf("failed to load persons: %w", err)
	}

	// Group by the canonical form — IDENTICAL to what names are stored under.
	byCanonical := map[string][]storage.DedupMember{}
	for _, m := range members {
		k := util.NormalizePersonName(m.Name)
		byCanonical[k] = append(byCanonical[k], m)
	}

	if !personsUnaccentApply {
		fmt.Println("DRY RUN — pass --apply to merge + canonicalize. Variant-duplicate groups:")
	}

	var dupGroups, mergedAway, skipped, renamed int
	for canonical, g := range byCanonical {
		if len(g) > 1 {
			dupGroups++
			survivor, ok := storage.ChooseCanonical(g)
			if !ok {
				skipped++
				log.Warn().Str("canonical", canonical).Int("members", len(g)).
					Msg("Skipping group (2+ curated members — won't merge real entities)")
				continue
			}
			var fromIDs []int64
			for _, m := range g {
				if m.ID != survivor.ID {
					fromIDs = append(fromIDs, m.ID)
				}
			}
			fmt.Printf("  %-45s survivor=#%d (facts=%d) <- %v\n", canonical, survivor.ID, survivor.FactCount, fromIDs)
			if personsUnaccentApply {
				for _, from := range fromIDs {
					if err := storer.MergePersons(ctx, from, survivor.ID); err != nil {
						return fmt.Errorf("merge %d into %d (%q): %w", from, survivor.ID, canonical, err)
					}
					mergedAway++
				}
			}
		}
	}

	if personsUnaccentApply {
		// Rewrite every remaining name to its canonical form (collision-free now
		// that all canonical-colliding rows were merged above).
		members, err = storer.GetAllDedupMembers(ctx)
		if err != nil {
			return fmt.Errorf("failed to reload persons: %w", err)
		}
		for _, m := range members {
			if c := util.NormalizePersonName(m.Name); c != m.Name {
				if err := storer.RenamePerson(ctx, m.ID, c); err != nil {
					return fmt.Errorf("canonicalize %d: %w", m.ID, err)
				}
				renamed++
			}
		}
		log.Info().Int("dup_groups", dupGroups).Int("rows_merged_away", mergedAway).
			Int("groups_skipped", skipped).Int("names_canonicalized", renamed).Msg("Unaccent complete")
		fmt.Printf("\nDone: %d dup groups, %d rows merged away, %d skipped, %d names canonicalized.\n",
			dupGroups, mergedAway, skipped, renamed)
	} else {
		fmt.Printf("\n%d variant-dup groups (%d would be skipped as 2+ curated). Re-run with --apply.\n", dupGroups, skipped)
	}
	return nil
}
