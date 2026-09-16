package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/util"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	stancesBackfillPerson string
	stancesBackfillDryRun bool
)

var stancesBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Backfill stance documents from existing facts",
	Long: `Assigns each existing fact to a stance (creating new stances as needed) using
the same per-fact LLM topic-assignment path as production ingest, then runs the
same LLM-vetted merge sweep the daily pipeline uses (Phase A agglomerative
clustering of centroids + Phase B per-cluster LLM verification + post-merge
contradiction re-detection).

For each person (or specified person):
1. Loads all active facts with embeddings.
2. Deduplicates by exact embedding_text so identical-label facts share one
   ProcessNewFact call (representative + cheap join inserts for duplicates).
3. For each unique label group, calls StanceManager.ProcessNewFact on the
   representative — this runs assignTopic + (when matched) the analyze-vs-stance
   call, mirroring ingest behaviour.
4. After all facts are placed, runs the same per-person merge the daily
   pipeline uses. To tune merge thresholds run
   ` + "`dm stances merge --recall-threshold ...`" + ` afterwards.

Examples:
  # Backfill all persons
  ./dm stances backfill

  # Backfill specific person
  ./dm stances backfill --person "Marine Le Pen"

  # Dry run
  ./dm stances backfill --person "Marine Le Pen" --dry-run`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runStancesBackfill,
}

func init() {
	stancesCmd.AddCommand(stancesBackfillCmd)
	stancesBackfillCmd.Flags().StringVar(&stancesBackfillPerson, "person", "", "Specific person name to backfill (optional)")
	stancesBackfillCmd.Flags().BoolVar(&stancesBackfillDryRun, "dry-run", false, "Show what would be created without writing to database")
}

func runStancesBackfill(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	var stanceManager *core.StanceManager
	if !stancesBackfillDryRun {
		stanceManager, err = core.NewStanceManagerFromEnv(storer)
		if err != nil {
			return fmt.Errorf("failed to create stance manager: %w", err)
		}
	}

	persons, err := personsForCommand(ctx, storer, stancesBackfillPerson, storer.GetPersonsWithFacts)
	if err != nil {
		return err
	}

	log.Info().Int("person_count", len(persons)).Bool("dry_run", stancesBackfillDryRun).Msg("Starting stance backfill")

	var totalCreated, totalMerged, totalContradictions int

	for _, p := range persons {
		stats, err := core.BackfillStancesForPerson(ctx, storer, stanceManager, p, stancesBackfillDryRun)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Failed to backfill stances")
			continue
		}
		printBackfillDryRunGroups(stats)
		totalCreated += stats.StancesCreated
		totalMerged += stats.StancesMerged
		totalContradictions += stats.ContradictionsDetected
	}

	rows := []resultRow{
		{Label: "Persons processed", Value: int64(len(persons))},
		{Label: "Stances created", Value: int64(totalCreated)},
	}
	if !stancesBackfillDryRun {
		rows = append(rows,
			resultRow{Label: "Stances merged (centroid)", Value: int64(totalMerged)},
			resultRow{Label: "Contradictions detected (new)", Value: int64(totalContradictions)},
		)
	}
	printResults("STANCE BACKFILL RESULTS", rows)
	if stancesBackfillDryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}

	return nil
}

// printBackfillDryRunGroups shows the exact-label groups a dry-run backfill
// would route (empty outside dry-run).
func printBackfillDryRunGroups(stats core.BackfillStats) {
	for _, g := range stats.DryRunGroups {
		fmt.Printf("  [DRY-RUN] Would process group %q (%d facts, rep ID=%d)\n", util.TruncateString(g.Label, 80), g.FactCount, g.RepresentativeID)
	}
}
