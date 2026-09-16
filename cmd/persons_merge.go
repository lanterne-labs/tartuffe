package cmd

import (
	"context"
	"digital-mirror/backend/deps"
	"fmt"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	personsMergeFrom int64
	personsMergeInto int64
)

// personsMergeCmd merges one person row into another, repointing all FK
// references and recomputing the survivor's score.
var personsMergeCmd = &cobra.Command{
	Use:   "merge",
	Short: "Merge one person into another (--from <id> --into <id>).",
	Long: `Folds the --from person into the --into person: every fact mention,
contradiction, stance, twitter handle, search-queue entry, link, and log row
referencing --from is repointed to --into (colliding rows are dropped), the
--from person row is deleted, and --into's honesty score is recomputed.

  dm persons merge --from 42 --into 7`,
	Args:        cobra.NoArgs,
	Annotations: map[string]string{"needs_db": "true"},
	Run:         runPersonsMerge,
}

func init() {
	personsCmd.AddCommand(personsMergeCmd)
	personsMergeCmd.Flags().Int64Var(&personsMergeFrom, "from", 0, "ID of the person to merge away (will be deleted).")
	personsMergeCmd.Flags().Int64Var(&personsMergeInto, "into", 0, "ID of the surviving person.")
	_ = personsMergeCmd.MarkFlagRequired("from")
	_ = personsMergeCmd.MarkFlagRequired("into")
}

func runPersonsMerge(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading.")
	}

	if personsMergeFrom <= 0 || personsMergeInto <= 0 {
		log.Fatal().Msg("--from and --into must be positive person IDs")
	}
	if personsMergeFrom == personsMergeInto {
		log.Fatal().Msg("--from and --into must differ")
	}

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer")
	}
	defer storer.Close()

	if err := storer.MergePersons(ctx, personsMergeFrom, personsMergeInto); err != nil {
		log.Fatal().Err(err).Int64("from", personsMergeFrom).Int64("into", personsMergeInto).Msg("Merge failed")
	}

	fmt.Printf("Merged person %d into %d.\n", personsMergeFrom, personsMergeInto)
}
