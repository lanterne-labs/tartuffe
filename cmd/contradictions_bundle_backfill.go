package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"
	"fmt"
	"os"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	bundleBackfillPersonName string
	bundleBackfillPersonID   int64
	bundleBackfillMax        int
	bundleBackfillNoLLM      bool
	bundleBackfillDryRun     bool
)

// contradictionsBundleBackfillCmd backfills bundle_stance_id on existing contradictions.
//
// Two phases:
//  1. Fast SQL pass: set bundle_stance_id where both facts now share a stance via
//     stance_document_facts. Mirrors migration 20260503000000 — necessary because the
//     migration ran before stances were retroactively populated for older facts.
//  2. LLM Bundler pass (skip with --no-llm): for remaining solo contradictions, embed
//     the contradiction summary, retrieve top-K candidate stances per person, and ask
//     the LLM YES/NO whether each is the underlying-inconsistency bundle.
var contradictionsBundleBackfillCmd = &cobra.Command{
	Use:   "bundle-backfill",
	Short: "Populate bundle_stance_id on existing contradictions (SQL fast path + LLM bundler).",
	Long: `Walks contradictions whose bundle_stance_id is NULL and attempts to assign one.

Phase 1 (always runs, instant): SQL UPDATE that sets bundle_stance_id where both
facts now share a stance via stance_document_facts. Run this after `+"`./dm stances rebuild`"+`
to retro-bundle contradictions whose facts were stance-less before the rebuild.

Phase 2 (skip with --no-llm): for the remaining solos, run the LLM Bundler. Each
contradiction costs one embedding + up to top-K (3) cheap chat calls.

Examples:
  ./dm contradictions bundle-backfill                    # both phases, all persons
  ./dm contradictions bundle-backfill --no-llm           # SQL only
  ./dm contradictions bundle-backfill --person-id 28     # scope to a person
  ./dm contradictions bundle-backfill --max 50           # cap LLM phase
  ./dm contradictions bundle-backfill --dry-run          # report only, no writes`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runBundleBackfill,
}

func init() {
	contradictionsCmd.AddCommand(contradictionsBundleBackfillCmd)
	contradictionsBundleBackfillCmd.Flags().StringVarP(&bundleBackfillPersonName, "person", "p", "", "Restrict to a single person by name")
	contradictionsBundleBackfillCmd.Flags().Int64Var(&bundleBackfillPersonID, "person-id", 0, "Restrict to a single person by numeric ID (overrides --person)")
	contradictionsBundleBackfillCmd.Flags().IntVar(&bundleBackfillMax, "max", 0, "Maximum number of solo contradictions to process in the LLM phase (0 = no cap)")
	contradictionsBundleBackfillCmd.Flags().BoolVar(&bundleBackfillNoLLM, "no-llm", false, "Skip the LLM Bundler phase, run only the SQL fast path")
	contradictionsBundleBackfillCmd.Flags().BoolVar(&bundleBackfillDryRun, "dry-run", false, "Show counts without writing anything")
}

func runBundleBackfill(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	var personIDPtr *int64
	switch {
	case bundleBackfillPersonID > 0:
		id := bundleBackfillPersonID
		personIDPtr = &id
	case bundleBackfillPersonName != "":
		normalizedName := util.NormalizeString(bundleBackfillPersonName)
		pid, err := storer.GetPersonIDByName(ctx, normalizedName)
		if err != nil {
			return fmt.Errorf("failed to find person %q: %w", bundleBackfillPersonName, err)
		}
		personIDPtr = &pid
	}

	// Pre-count solo contradictions so we can report deltas.
	preSolos, err := storer.ListSoloContradictionsForBundling(ctx, personIDPtr, 0)
	if err != nil {
		return fmt.Errorf("count solos before fast path: %w", err)
	}
	log.Info().Int("solo_contradictions", len(preSolos)).Msg("Bundle backfill starting")

	// Phase 1: SQL fast path.
	if bundleBackfillDryRun {
		log.Info().Msg("Dry run: skipping fast-path UPDATE")
	} else {
		updated, err := storer.BundleBackfillFastPath(ctx, personIDPtr)
		if err != nil {
			return fmt.Errorf("phase 1 fast path: %w", err)
		}
		log.Info().Int64("rows_updated", updated).Msg("Phase 1 (SQL fast path) complete")
	}

	if bundleBackfillNoLLM {
		log.Info().Msg("--no-llm set, skipping Bundler phase")
		return reportRemaining(ctx, storer, personIDPtr)
	}

	// Phase 2: LLM Bundler over remaining solos.
	remaining, err := storer.ListSoloContradictionsForBundling(ctx, personIDPtr, bundleBackfillMax)
	if err != nil {
		return fmt.Errorf("list remaining solos: %w", err)
	}
	if len(remaining) == 0 {
		fmt.Fprintln(os.Stderr, "No remaining solo contradictions; nothing to bundle.")
		return nil
	}
	log.Info().Int("remaining_solos", len(remaining)).Msg("Phase 2: starting LLM Bundler pass")

	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("phase 2 requires embedding generator: %w (rerun with --no-llm to skip)", err)
	}
	bundler, err := contradiction.NewBundlerFromEnv(storer, embedder)
	if err != nil {
		return fmt.Errorf("phase 2 requires bundler: %w (rerun with --no-llm to skip)", err)
	}

	resolved := 0
	failed := 0
	for i, row := range remaining {
		if bundleBackfillDryRun {
			log.Info().
				Int("idx", i+1).
				Int("total", len(remaining)).
				Int64("contradiction_id", row.ID).
				Int64("person_id", row.PersonID).
				Msg("Dry run: would call Bundler.Resolve")
			continue
		}
		if err := bundler.Resolve(ctx, row.PersonID, row.ID, row.FactAID, row.FactBID, row.FactAText, row.FactBText, row.Reason); err != nil {
			failed++
			log.Warn().Err(err).Int64("contradiction_id", row.ID).Int64("person_id", row.PersonID).Msg("Bundler.Resolve failed; row stays solo")
			continue
		}
		resolved++
		if (i+1)%25 == 0 {
			log.Info().Int("processed", i+1).Int("total", len(remaining)).Int("resolved_so_far", resolved).Msg("Bundler progress")
		}
	}
	log.Info().Int("attempted", len(remaining)).Int("resolved", resolved).Int("failed", failed).Msg("Phase 2 complete")

	return reportRemaining(ctx, storer, personIDPtr)
}

func reportRemaining(ctx context.Context, storer *storage.PostgresStorer, personIDPtr *int64) error {
	final, err := storer.ListSoloContradictionsForBundling(ctx, personIDPtr, 0)
	if err != nil {
		return fmt.Errorf("count solos after backfill: %w", err)
	}
	fmt.Fprintf(os.Stderr, "Remaining solo contradictions: %d\n", len(final))
	return nil
}
