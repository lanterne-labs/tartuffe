package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/deps"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// personsRescoreCmd recomputes every honesty score, for when the recompute at
// the end of a long run (a fold, a deletion) failed.
var personsRescoreCmd = &cobra.Command{
	Use:         "rescore",
	Short:       "Recompute the honesty score of every person and party",
	Args:        cobra.NoArgs,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runPersonsRescore,
}

func init() {
	personsCmd.AddCommand(personsRescoreCmd)
}

func runPersonsRescore(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading")
	}
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	updates, err := storer.RecomputeAllHonestyScores(ctx, false)
	if err != nil {
		return fmt.Errorf("recomputing honesty scores: %w", err)
	}
	fmt.Printf("Recomputed %d honesty scores\n", len(updates))
	return nil
}
