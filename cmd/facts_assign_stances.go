package cmd

import (
	"context"
	"digital-mirror/backend/contradiction"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"digital-mirror/backend/embedding"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/storage"
	"fmt"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	assignStancesPerson            string
	assignStancesPersonsFile       string
	assignStancesLimit             int
	assignStancesDryRun            bool
	assignStancesSkipFinalRebundle bool
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
  # Assign all orphan facts across the DB
  ./dm facts assign-stances

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
	factsAssignStancesCmd.Flags().IntVar(&assignStancesLimit, "limit", 0, "Max (person, fact) routings this run (0 = no cap); leftovers stay pending")
	factsAssignStancesCmd.Flags().BoolVar(&assignStancesDryRun, "dry-run", false, "Show planned work without writing to the database")
	factsAssignStancesCmd.Flags().BoolVar(&assignStancesSkipFinalRebundle, "skip-final-rebundle", false, "Skip the DB-wide rebundle pass at the end")
}

type assignStats struct {
	personsProcessed     int64
	personsSkippedNoOrph int64 // orphans gone between listing and processing, or run budget exhausted
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

	storer, err := deps.OpenStorer(ctx)
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

	var stanceManager *core.StanceManager
	var bundler *contradiction.Bundler
	if !assignStancesDryRun {
		stanceManager, err = core.NewStanceManagerFromEnv(storer)
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

	log.Info().
		Int("person_count", len(persons)).
		Bool("dry_run", assignStancesDryRun).
		Bool("skip_final_rebundle", assignStancesSkipFinalRebundle).
		Msg("Starting orphan-fact stance assignment")

	var budget *int64
	if assignStancesLimit > 0 {
		b := int64(assignStancesLimit)
		budget = &b
	}

	rebundle := personRebundler(storer, bundler)

	var stats assignStats
	processOne := func(p core.PersonRecord) {
		ps, err := core.AssignOrphansForPerson(ctx, storer, stanceManager, rebundle, p, assignStancesDryRun, budget)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("assign-stances failed for person")
			return
		}
		atomic.AddInt64(&stats.personsProcessed, 1)
		if ps.Skipped {
			atomic.AddInt64(&stats.personsSkippedNoOrph, 1)
		}
		atomic.AddInt64(&stats.orphansAssigned, ps.OrphansAssigned)
		atomic.AddInt64(&stats.orphanFailures, ps.OrphanFailures)
		atomic.AddInt64(&stats.verdictNewStance, ps.VerdictNewStance)
		atomic.AddInt64(&stats.verdictDuplicate, ps.VerdictDuplicate)
		atomic.AddInt64(&stats.verdictNewInfo, ps.VerdictNewInfo)
		atomic.AddInt64(&stats.verdictContradiction, ps.VerdictContradiction)
		atomic.AddInt64(&stats.rebundleSQL, ps.RebundleSQL)
		atomic.AddInt64(&stats.rebundleLLM, ps.RebundleLLM)
		atomic.AddInt64(&stats.rebundleSolosLeft, ps.RebundleSolosLeft)
	}

	// As many persons at once as the endpoint serves (llm.ForEach).
	_ = llm.ForEach(ctx, persons, func(ctx context.Context, p core.PersonRecord) error {
		processOne(p)
		return nil
	})

	rows := []resultRow{
		{Label: "Persons processed", Value: stats.personsProcessed},
		{Label: "Orphan facts assigned", Value: stats.orphansAssigned},
		{Label: "  → NEW_STANCE", Value: stats.verdictNewStance},
		{Label: "  → DUPLICATE", Value: stats.verdictDuplicate},
		{Label: "  → NEW_INFO", Value: stats.verdictNewInfo},
		{Label: "  → CONTRADICTION", Value: stats.verdictContradiction},
		{Label: "Orphan failures (left orphan)", Value: stats.orphanFailures},
	}
	if !assignStancesDryRun {
		rows = append(rows,
			resultRow{Label: "Per-person rebundle (SQL)", Value: stats.rebundleSQL},
			resultRow{Label: "Per-person rebundle (LLM)", Value: stats.rebundleLLM},
			resultRow{Label: "Per-person solos still left", Value: stats.rebundleSolosLeft},
		)
	}
	printResults("ORPHAN-FACT ASSIGN RESULTS", rows)
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
func resolveTargetPersons(ctx context.Context, storer *storage.PostgresStorer) ([]core.PersonRecord, error) {
	if assignStancesPersonsFile != "" {
		return resolvePersonsFromFile(ctx, storer, assignStancesPersonsFile)
	}
	if assignStancesPerson != "" {
		p, err := core.ResolvePersonID(ctx, storer, assignStancesPerson, core.ExactMatchOnly)
		if err != nil {
			return nil, err
		}
		return []core.PersonRecord{p}, nil
	}
	return storer.ListPersonsWithOrphanFacts(ctx)
}

// personRebundler adapts contradiction.RebundlePerson to the callback core's
// stance sweeps take. The bundler may be nil in dry-run mode: the callback is
// never invoked then.
func personRebundler(storer *storage.PostgresStorer, bundler *contradiction.Bundler) core.PersonRebundler {
	return func(ctx context.Context, p core.PersonRecord) (fast, llm, failed int64) {
		return contradiction.RebundlePerson(ctx, storer, bundler, toPersonInfo(p))
	}
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

	log.Info().Msg("Starting DB-wide final rebundle")
	fast, llm, failed := contradiction.RebundleAll(ctx, storer, bundler)
	printResults("DB-WIDE FINAL REBUNDLE", []resultRow{
		{Label: "Rebundled (SQL fast path)", Value: fast},
		{Label: "Rebundled (LLM bundler)", Value: llm},
		{Label: "Solo contradictions remaining", Value: failed, Note: "(LLM declined or errored)"},
	})
	return nil
}
