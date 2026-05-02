package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	stancesRebuildPerson   string
	stancesRebuildDryRun   bool
	stancesRebuildResume   bool
	stancesRebuildParallel int
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

  # Rebuild all stances with 8 persons in flight at once
  ./dm stances rebuild --resume --parallel 8

  # Dry run
  ./dm stances rebuild --person "Marine Le Pen" --dry-run`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runStancesRebuild,
}

func init() {
	stancesCmd.AddCommand(stancesRebuildCmd)
	stancesRebuildCmd.Flags().StringVar(&stancesRebuildPerson, "person", "", "Specific person name to rebuild (optional)")
	stancesRebuildCmd.Flags().BoolVar(&stancesRebuildDryRun, "dry-run", false, "Show what would be done without writing to database")
	stancesRebuildCmd.Flags().BoolVar(&stancesRebuildResume, "resume", false, "Skip persons that already have at least one stance document (for resuming an interrupted rebuild)")
	stancesRebuildCmd.Flags().IntVar(&stancesRebuildParallel, "parallel", 1, "Number of persons to rebuild concurrently (default 1, sequential). Each in-flight person consumes 1-2 chat-LLM slots.")
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

	persons, err := getPersonsForBackfill(ctx, storer, stancesRebuildPerson)
	if err != nil {
		return err
	}

	if stancesRebuildParallel < 1 {
		stancesRebuildParallel = 1
	}

	log.Info().
		Int("person_count", len(persons)).
		Int("parallel", stancesRebuildParallel).
		Bool("dry_run", stancesRebuildDryRun).
		Msg("Starting stance rebuild")

	var totalDeleted, totalCreated, skipped int64

	processOne := func(p storage.PersonInfo) {
		existing, err := adapter.GetStanceDocumentsForPerson(ctx, p.ID)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Failed to get existing stances")
			return
		}

		if stancesRebuildResume && len(existing) > 0 {
			atomic.AddInt64(&skipped, 1)
			return
		}

		if len(existing) > 0 {
			if stancesRebuildDryRun {
				fmt.Printf("[DRY-RUN] Would delete %d existing stances for %s\n", len(existing), p.Name)
			} else {
				if err := adapter.DeleteStanceDocumentsForPerson(ctx, p.ID); err != nil {
					log.Error().Err(err).Str("person", p.Name).Msg("Failed to delete existing stances")
					return
				}
				log.Info().Str("person", p.Name).Int("deleted", len(existing)).Msg("Deleted existing stances")
			}
			atomic.AddInt64(&totalDeleted, int64(len(existing)))
		}

		created, _, err := backfillStancesForPerson(ctx, storer, adapter, stanceManager, p.ID, p.Name, stancesRebuildDryRun)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Failed to rebuild stances")
			return
		}
		atomic.AddInt64(&totalCreated, int64(created))
	}

	jobs := make(chan storage.PersonInfo)
	var wg sync.WaitGroup
	for i := 0; i < stancesRebuildParallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for p := range jobs {
				processOne(p)
			}
		}()
	}
	for _, p := range persons {
		jobs <- p
	}
	close(jobs)
	wg.Wait()

	processed := int64(len(persons)) - skipped
	fmt.Println()
	fmt.Println("=== STANCE REBUILD RESULTS ===")
	fmt.Printf("Persons processed:        %d\n", processed)
	fmt.Printf("Persons skipped (resume): %d\n", skipped)
	fmt.Printf("Stances deleted:          %d\n", totalDeleted)
	fmt.Printf("Stances created:          %d\n", totalCreated)
	if stancesRebuildDryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}

	return nil
}
