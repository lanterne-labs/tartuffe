package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"

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
	Long: `Finds person rows whose names collide under util.PersonNameKey (lowercase,
accent-stripped, every space and punctuation mark removed) — e.g. "éric ciotti" +
"eric ciotti", "d'azur" + "d'azur", or "olivier fa-ure" + "olivier faure". Merges each group into a single survivor (curated/most-facts
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

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	res, err := core.UnaccentPersons(ctx, storer, personsUnaccentApply)
	if err != nil {
		return err
	}

	if !personsUnaccentApply {
		fmt.Println("DRY RUN — pass --apply to merge + canonicalize. Variant-duplicate groups:")
	}
	for _, g := range res.Groups {
		if g.Skipped {
			continue
		}
		fmt.Printf("  %-45s survivor=#%d (facts=%d) <- %v\n", g.Canonical, g.Survivor.ID, g.Survivor.FactCount, g.FromIDs)
	}

	if personsUnaccentApply {
		fmt.Printf("\nDone: %d dup groups, %d rows merged away, %d skipped, %d names canonicalized.\n",
			res.DupGroups, res.MergedAway, res.Skipped, res.Renamed)
	} else {
		fmt.Printf("\n%d variant-dup groups (%d would be skipped as 2+ curated). Re-run with --apply.\n", res.DupGroups, res.Skipped)
	}
	return nil
}
