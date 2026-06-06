package cmd

import (
	"context"
	"digital-mirror/backend/storage"
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
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	pairs, repointed, dropped, err := repointDuplicates(ctx, storer, repointDuplicatesApply)
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

// repointDuplicates finds every 'duplicate' fact that still anchors a
// contradiction and (when apply) repoints those contradictions onto the
// canonical fact. Returns (duplicatePairsProcessed, repointed, dropped). Shared
// by the CLI command and the daily-pipeline cleanup step. A per-pair failure is
// logged and skipped so one bad pair never aborts the sweep.
func repointDuplicates(ctx context.Context, storer *storage.PostgresStorer, apply bool) (int, int, int, error) {
	pairs, err := storer.GetDuplicatesNeedingRepoint(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to list duplicates needing repoint: %w", err)
	}
	if len(pairs) == 0 {
		log.Info().Msg("Repoint duplicates: no duplicate facts anchor contradictions, nothing to do")
		return 0, 0, 0, nil
	}
	if !apply {
		return len(pairs), 0, 0, nil
	}

	var totalRepointed, totalDropped, processed int
	for _, p := range pairs {
		repointed, dropped, rErr := storer.RepointFactDuplicate(ctx, p.General, p.Specific)
		if rErr != nil {
			log.Error().Err(rErr).Int64("general", p.General).Int64("specific", p.Specific).
				Msg("Repoint duplicates: failed for pair, skipping")
			continue
		}
		processed++
		totalRepointed += repointed
		totalDropped += dropped
		log.Info().Int64("general", p.General).Int64("specific", p.Specific).
			Int("repointed", repointed).Int("dropped", dropped).
			Msg("Repointed contradictions off duplicate fact")
	}
	return processed, totalRepointed, totalDropped, nil
}
