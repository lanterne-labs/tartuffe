package cmd

import (
	"context"
	"digital-mirror/backend/deps"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var repointDuplicatesApply bool

var factsRepointDuplicatesCmd = &cobra.Command{
	Use:   "repoint-duplicates",
	Short: "Repoint contradictions off facts that have been marked as duplicates.",
	Long: `When a fact is marked a duplicate (fact_relations 'duplicate'), it is hidden
from active queries but any contradictions it already anchored still reference it
and still count toward the honesty score. This moves those contradictions onto
the canonical fact (deduping/dropping self- and redundant pairs) and recomputes
affected scores. READ-ONLY by default; pass --apply to write.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runFactsRepointDuplicates,
}

func init() {
	factsCmd.AddCommand(factsRepointDuplicatesCmd)
	factsRepointDuplicatesCmd.Flags().BoolVar(&repointDuplicatesApply, "apply", false, "Apply the repointing (default is a read-only report)")
}

func runFactsRepointDuplicates(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}
	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	pairs, repointed, dropped, err := storer.RepointDuplicates(ctx, repointDuplicatesApply)
	if err != nil {
		return err
	}
	if repointDuplicatesApply {
		fmt.Fprintf(os.Stderr, "Repoint duplicates: %d duplicate facts processed, %d contradictions repointed, %d dropped.\n", pairs, repointed, dropped)
	} else {
		fmt.Fprintf(os.Stderr, "[DRY RUN] %d duplicate facts have contradictions to repoint. Re-run with --apply to write.\n", pairs)
	}
	return nil
}
