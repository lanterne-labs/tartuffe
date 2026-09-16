package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/embedding"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	stancesRebuildPerson      string
	stancesRebuildPersonsFile string
	stancesRebuildDryRun      bool
	stancesRebuildResume      bool
	stancesRebuildParallel    int
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
	stancesRebuildCmd.Flags().StringVar(&stancesRebuildPersonsFile, "persons-file", "", "Path to a file with one person name per line (mutually exclusive with --person)")
	stancesRebuildCmd.Flags().BoolVar(&stancesRebuildDryRun, "dry-run", false, "Show what would be done without writing to database")
	stancesRebuildCmd.Flags().BoolVar(&stancesRebuildResume, "resume", false, "Skip persons that already have at least one stance document (for resuming an interrupted rebuild)")
	stancesRebuildCmd.Flags().IntVar(&stancesRebuildParallel, "parallel", 1, "Number of persons to rebuild concurrently (default 1, sequential). Each in-flight person consumes 1-2 chat-LLM slots.")
}

func runStancesRebuild(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	var stanceManager *core.StanceManager
	var bundler *contradiction.Bundler
	if !stancesRebuildDryRun {
		stanceManager, err = core.NewStanceManagerFromEnv(storer)
		if err != nil {
			return fmt.Errorf("failed to create stance manager: %w", err)
		}

		// Bundler is required so that after rebuilding stances we can immediately
		// rebundle the contradictions whose bundle_stance_id was wiped by the FK
		// ON DELETE SET NULL when stances were dropped. Without this, a rebuild
		// would leave the system in an inconsistent state (correct stances, all
		// contradictions solo) until a separate command was run.
		embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
		if err != nil {
			return fmt.Errorf("failed to create embedding generator (required for per-person bundle phase): %w", err)
		}
		bundler, err = contradiction.NewBundlerFromEnv(storer, embedder)
		if err != nil {
			return fmt.Errorf("failed to create bundler (required for per-person bundle phase): %w", err)
		}
	}

	if stancesRebuildPerson != "" && stancesRebuildPersonsFile != "" {
		return fmt.Errorf("--person and --persons-file are mutually exclusive")
	}
	var persons []core.PersonRecord
	if stancesRebuildPersonsFile != "" {
		persons, err = resolvePersonsFromFile(ctx, storer, stancesRebuildPersonsFile)
	} else {
		persons, err = personsForCommand(ctx, storer, stancesRebuildPerson, storer.GetPersonsWithFacts)
	}
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
	var totalMerged, totalContradictions int64
	var totalRebundledFast, totalRebundledLLM, totalBundleFailed int64

	rebundle := personRebundler(storer, bundler)
	opts := core.RebuildOptions{DryRun: stancesRebuildDryRun, Resume: stancesRebuildResume}

	processOne := func(p core.PersonRecord) {
		st, err := core.RebuildStancesForPerson(ctx, storer, stanceManager, rebundle, p, opts)
		// Deleted is set even when the backfill after it failed.
		atomic.AddInt64(&totalDeleted, int64(st.Deleted))
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Failed to rebuild stances")
			return
		}
		if st.Skipped {
			atomic.AddInt64(&skipped, 1)
			return
		}
		if stancesRebuildDryRun && st.Deleted > 0 {
			fmt.Printf("[DRY-RUN] Would delete %d existing stances for %s\n", st.Deleted, p.Name)
		}
		printBackfillDryRunGroups(st.Backfill)
		atomic.AddInt64(&totalCreated, int64(st.Backfill.StancesCreated))
		atomic.AddInt64(&totalMerged, int64(st.Backfill.StancesMerged))
		atomic.AddInt64(&totalContradictions, int64(st.Backfill.ContradictionsDetected))
		atomic.AddInt64(&totalRebundledFast, st.RebundledFast)
		atomic.AddInt64(&totalRebundledLLM, st.RebundledLLM)
		atomic.AddInt64(&totalBundleFailed, st.BundleFailed)
	}

	jobs := make(chan core.PersonRecord)
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
	rows := []resultRow{
		{Label: "Persons processed", Value: processed},
		{Label: "Persons skipped (resume)", Value: skipped},
		{Label: "Stances deleted", Value: totalDeleted},
		{Label: "Stances created", Value: totalCreated},
	}
	if !stancesRebuildDryRun {
		rows = append(rows,
			resultRow{Label: "Stances merged (centroid)", Value: totalMerged},
			resultRow{Label: "Contradictions detected (new)", Value: totalContradictions},
			resultRow{Label: "Contradictions rebundled (SQL)", Value: totalRebundledFast},
			resultRow{Label: "Contradictions rebundled (LLM)", Value: totalRebundledLLM},
		)
		if totalBundleFailed > 0 {
			rows = append(rows, resultRow{Label: "Contradictions left solo", Value: totalBundleFailed, Note: "(LLM bundler declined or failed)"})
		}
	}
	printResults("STANCE REBUILD RESULTS", rows)
	if stancesRebuildDryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}

	return nil
}
