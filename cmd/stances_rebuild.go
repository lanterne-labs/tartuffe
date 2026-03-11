package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"fmt"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	stancesRebuildPerson string
	stancesRebuildDryRun bool
)

var stancesRebuildCmd = &cobra.Command{
	Use:   "rebuild",
	Short: "Delete and rebuild stance documents from facts",
	Long: `Deletes all existing stance documents for a person (or all persons)
and rebuilds them from the underlying facts using the backfill logic.

This is useful when the clustering algorithm or LLM prompts have changed
and you want to regenerate all stances from scratch.

Examples:
  # Rebuild stances for a specific person
  ./dm stances rebuild --person "Marine Le Pen"

  # Rebuild all stances
  ./dm stances rebuild

  # Dry run
  ./dm stances rebuild --person "Marine Le Pen" --dry-run`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runStancesRebuild,
}

func init() {
	stancesCmd.AddCommand(stancesRebuildCmd)
	stancesRebuildCmd.Flags().StringVar(&stancesRebuildPerson, "person", "", "Specific person name to rebuild (optional)")
	stancesRebuildCmd.Flags().BoolVar(&stancesRebuildDryRun, "dry-run", false, "Show what would be done without writing to database")
}

func runStancesRebuild(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	adapter := storage.NewStanceStorerAdapter(storer)

	var stanceManager *core.StanceManager
	if !stancesRebuildDryRun {
		stanceManager, err = core.NewStanceManagerFromEnv(adapter)
		if err != nil {
			return fmt.Errorf("failed to create stance manager: %w", err)
		}
	}

	// Determine which persons to process
	persons, err := getPersonsForBackfill(ctx, storer, stancesRebuildPerson)
	if err != nil {
		return err
	}

	log.Info().Int("person_count", len(persons)).Bool("dry_run", stancesRebuildDryRun).Msg("Starting stance rebuild")

	totalDeleted := 0
	totalCreated := 0

	for _, p := range persons {
		// Count existing stances before deletion
		existing, err := adapter.GetStanceDocumentsForPerson(ctx, p.ID)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Failed to get existing stances")
			continue
		}

		if len(existing) > 0 {
			if stancesRebuildDryRun {
				fmt.Printf("[DRY-RUN] Would delete %d existing stances for %s\n", len(existing), p.Name)
			} else {
				if err := adapter.DeleteStanceDocumentsForPerson(ctx, p.ID); err != nil {
					log.Error().Err(err).Str("person", p.Name).Msg("Failed to delete existing stances")
					continue
				}
				log.Info().Str("person", p.Name).Int("deleted", len(existing)).Msg("Deleted existing stances")
			}
			totalDeleted += len(existing)
		}

		// Run backfill
		created, _, err := backfillStancesForPerson(ctx, storer, adapter, stanceManager, p.ID, p.Name, stancesRebuildDryRun)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Failed to rebuild stances")
			continue
		}
		totalCreated += created
	}

	fmt.Println()
	fmt.Println("=== STANCE REBUILD RESULTS ===")
	fmt.Printf("Persons processed:     %d\n", len(persons))
	fmt.Printf("Stances deleted:       %d\n", totalDeleted)
	fmt.Printf("Stances created:       %d\n", totalCreated)
	if stancesRebuildDryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}

	return nil
}
