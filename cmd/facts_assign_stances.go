package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/storage"
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	assignStancesPerson             string
	assignStancesPersonsFile        string
	assignStancesParallel           int
	assignStancesDryRun             bool
	assignStancesSkipFinalRebundle  bool
)

var factsAssignStancesCmd = &cobra.Command{
	Use:   "assign-stances",
	Short: "Assign every fact-without-stance to a stance, then rebundle solo contradictions",
	Long: `Finds facts that are not linked to any stance_document (the
"facts_without_stance_total" invariant) and routes each one through the live
ingest path: ProcessNewFact picks an existing stance for the person via the
LLM topic-classifier, or creates a new stance when no existing topic fits.

After every orphan for a person is assigned:
  1. BundleBackfillFastPath fills bundle_stance_id on any contradiction whose
     two facts now share a stance (free SQL).
  2. The LLM Bundler processes any contradiction still flagged solo.

After every in-scope person is processed, the same two-phase rebundle is
also run DB-wide (unless --skip-final-rebundle is set), to catch
contradictions across persons that the per-person pass missed.

Idempotent per-fact: a crash mid-run leaves a self-consistent partial state
(AddFactToStance is ON CONFLICT no-op; RecordContradiction is unique-
constrained on (fact_a_id, fact_b_id)). Re-running picks up exactly the
still-orphan facts.

Examples:
  # Assign all orphan facts across the DB, 4 persons in flight
  ./dm facts assign-stances --parallel 4

  # Single person
  ./dm facts assign-stances --person "Marine Le Pen"

  # Dry-run: report orphan counts + planned rebundle scope, no writes
  ./dm facts assign-stances --dry-run`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runFactsAssignStances,
}

func init() {
	factsCmd.AddCommand(factsAssignStancesCmd)
	factsAssignStancesCmd.Flags().StringVar(&assignStancesPerson, "person", "", "Specific person name (mutually exclusive with --persons-file)")
	factsAssignStancesCmd.Flags().StringVar(&assignStancesPersonsFile, "persons-file", "", "Path to a file with one person name per line")
	factsAssignStancesCmd.Flags().IntVar(&assignStancesParallel, "parallel", 1, "Number of persons to process concurrently (default 1)")
	factsAssignStancesCmd.Flags().BoolVar(&assignStancesDryRun, "dry-run", false, "Show planned work without writing to the database")
	factsAssignStancesCmd.Flags().BoolVar(&assignStancesSkipFinalRebundle, "skip-final-rebundle", false, "Skip the DB-wide rebundle pass at the end")
}

type assignStats struct {
	personsProcessed     int64
	personsSkippedNoOrph int64 // safety: shouldn't happen with the orphan-driven person list
	orphansAssigned      int64
	orphanFailures       int64
	verdictNewStance     int64
	verdictDuplicate     int64
	verdictNewInfo       int64
	verdictContradiction int64
	rebundleSQL          int64
	rebundleLLM          int64
	rebundleSolosLeft    int64
}

func runFactsAssignStances(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("creating storer: %w", err)
	}
	defer storer.Close()

	if assignStancesPerson != "" && assignStancesPersonsFile != "" {
		return fmt.Errorf("--person and --persons-file are mutually exclusive")
	}

	persons, err := resolveTargetPersons(ctx, storer)
	if err != nil {
		return err
	}
	if len(persons) == 0 {
		log.Info().Msg("No persons with orphan facts found — nothing to assign.")
		if !assignStancesSkipFinalRebundle && !assignStancesDryRun {
			log.Info().Msg("Running DB-wide rebundle anyway in case solo contradictions exist independent of orphans.")
			if err := runFinalRebundle(ctx, storer); err != nil {
				return err
			}
		}
		return nil
	}

	adapter := storage.NewStanceStorerAdapter(storer)

	var stanceManager *core.StanceManager
	var bundler *contradiction.Bundler
	if !assignStancesDryRun {
		stanceManager, err = core.NewStanceManagerFromEnv(adapter)
		if err != nil {
			return fmt.Errorf("creating stance manager: %w", err)
		}
		embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
		if err != nil {
			return fmt.Errorf("creating embedding generator (required for rebundle): %w", err)
		}
		bundler, err = contradiction.NewBundlerFromEnv(storer, embedder)
		if err != nil {
			return fmt.Errorf("creating bundler (required for rebundle): %w", err)
		}
	}

	if assignStancesParallel < 1 {
		assignStancesParallel = 1
	}

	log.Info().
		Int("person_count", len(persons)).
		Int("parallel", assignStancesParallel).
		Bool("dry_run", assignStancesDryRun).
		Bool("skip_final_rebundle", assignStancesSkipFinalRebundle).
		Msg("Starting orphan-fact stance assignment")

	var stats assignStats
	processOne := func(p storage.PersonInfo) {
		ps, err := assignOrphansForPerson(ctx, storer, stanceManager, bundler, p, assignStancesDryRun)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("assign-stances failed for person")
			return
		}
		atomic.AddInt64(&stats.personsProcessed, 1)
		if ps.skipped {
			atomic.AddInt64(&stats.personsSkippedNoOrph, 1)
		}
		atomic.AddInt64(&stats.orphansAssigned, ps.orphansAssigned)
		atomic.AddInt64(&stats.orphanFailures, ps.orphanFailures)
		atomic.AddInt64(&stats.verdictNewStance, ps.verdictNewStance)
		atomic.AddInt64(&stats.verdictDuplicate, ps.verdictDuplicate)
		atomic.AddInt64(&stats.verdictNewInfo, ps.verdictNewInfo)
		atomic.AddInt64(&stats.verdictContradiction, ps.verdictContradiction)
		atomic.AddInt64(&stats.rebundleSQL, ps.rebundleSQL)
		atomic.AddInt64(&stats.rebundleLLM, ps.rebundleLLM)
		atomic.AddInt64(&stats.rebundleSolosLeft, ps.rebundleSolosLeft)
	}

	jobs := make(chan storage.PersonInfo)
	var wg sync.WaitGroup
	for i := 0; i < assignStancesParallel; i++ {
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

	fmt.Println()
	fmt.Println("=== ORPHAN-FACT ASSIGN RESULTS ===")
	fmt.Printf("Persons processed:               %d\n", stats.personsProcessed)
	fmt.Printf("Orphan facts assigned:           %d\n", stats.orphansAssigned)
	fmt.Printf("  → NEW_STANCE:                  %d\n", stats.verdictNewStance)
	fmt.Printf("  → DUPLICATE:                   %d\n", stats.verdictDuplicate)
	fmt.Printf("  → NEW_INFO:                    %d\n", stats.verdictNewInfo)
	fmt.Printf("  → CONTRADICTION:               %d\n", stats.verdictContradiction)
	fmt.Printf("Orphan failures (left orphan):   %d\n", stats.orphanFailures)
	if !assignStancesDryRun {
		fmt.Printf("Per-person rebundle (SQL):       %d\n", stats.rebundleSQL)
		fmt.Printf("Per-person rebundle (LLM):       %d\n", stats.rebundleLLM)
		fmt.Printf("Per-person solos still left:     %d\n", stats.rebundleSolosLeft)
	}
	if assignStancesDryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
		return nil
	}

	if assignStancesSkipFinalRebundle {
		fmt.Println("\n--skip-final-rebundle set; not running the DB-wide rebundle pass.")
		return nil
	}

	return runFinalRebundle(ctx, storer)
}

// resolveTargetPersons returns the persons to process based on flags. If
// neither --person nor --persons-file is set, returns every person who has
// at least one orphan fact.
func resolveTargetPersons(ctx context.Context, storer *storage.PostgresStorer) ([]storage.PersonInfo, error) {
	if assignStancesPersonsFile != "" {
		return resolvePersonsFromFile(ctx, storer, assignStancesPersonsFile)
	}
	if assignStancesPerson != "" {
		return getPersonsForBackfill(ctx, storer, assignStancesPerson)
	}
	return storer.ListPersonsWithOrphanFacts(ctx)
}

// runFinalRebundle runs a DB-wide BundleBackfillFastPath followed by the LLM
// Bundler over every remaining solo contradiction. Constructs its own bundler
// (the per-person one might have been built in a different scope).
func runFinalRebundle(ctx context.Context, storer *storage.PostgresStorer) error {
	embedder, err := embedding.NewOpenAIEmbeddingGeneratorFromEnv()
	if err != nil {
		return fmt.Errorf("creating embedding generator for final rebundle: %w", err)
	}
	bundler, err := contradiction.NewBundlerFromEnv(storer, embedder)
	if err != nil {
		return fmt.Errorf("creating bundler for final rebundle: %w", err)
	}

	fmt.Println()
	fmt.Println("=== DB-WIDE FINAL REBUNDLE ===")
	fast, llm, failed := rebundleAll(ctx, storer, bundler)
	fmt.Printf("Rebundled (SQL fast path):       %d\n", fast)
	fmt.Printf("Rebundled (LLM bundler):         %d\n", llm)
	fmt.Printf("Solo contradictions remaining:   %d (LLM declined or errored)\n", failed)
	return nil
}

type personAssignStats struct {
	skipped              bool
	orphansAssigned      int64
	orphanFailures       int64
	verdictNewStance     int64
	verdictDuplicate     int64
	verdictNewInfo       int64
	verdictContradiction int64
	rebundleSQL          int64
	rebundleLLM          int64
	rebundleSolosLeft    int64
}

func assignOrphansForPerson(
	ctx context.Context,
	storer *storage.PostgresStorer,
	stanceManager *core.StanceManager,
	bundler *contradiction.Bundler,
	p storage.PersonInfo,
	dryRun bool,
) (personAssignStats, error) {
	var ps personAssignStats

	orphans, err := storer.ListOrphanFactsForPerson(ctx, p.ID)
	if err != nil {
		return ps, fmt.Errorf("loading orphans: %w", err)
	}
	if len(orphans) == 0 {
		ps.skipped = true
		return ps, nil
	}

	if dryRun {
		log.Info().Str("person", p.Name).Int("orphan_count", len(orphans)).Msg("[DRY-RUN] Would process orphan facts")
		return ps, nil
	}

	for _, f := range orphans {
		res, err := stanceManager.ProcessNewFact(ctx, p.ID, f.ID, f.FactText, f.Embedding, f.CanonicalVerb)
		if err != nil {
			log.Warn().Err(err).Str("person", p.Name).Int64("fact_id", f.ID).Msg("ProcessNewFact failed; fact stays orphan")
			ps.orphanFailures++
			continue
		}
		ps.orphansAssigned++
		switch res.Verdict {
		case "NEW_STANCE":
			ps.verdictNewStance++
		case "DUPLICATE":
			ps.verdictDuplicate++
		case "NEW_INFO":
			ps.verdictNewInfo++
		case "CONTRADICTION":
			ps.verdictContradiction++
		}
	}

	fast, llm, failed := rebundlePerson(ctx, storer, bundler, p)
	ps.rebundleSQL = fast
	ps.rebundleLLM = llm
	ps.rebundleSolosLeft = failed

	log.Info().
		Str("person", p.Name).
		Int("orphans", len(orphans)).
		Int64("assigned", ps.orphansAssigned).
		Int64("failed", ps.orphanFailures).
		Int64("rebundle_sql", ps.rebundleSQL).
		Int64("rebundle_llm", ps.rebundleLLM).
		Msg("assign-stances done for person")

	return ps, nil
}
