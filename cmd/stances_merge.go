package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
	"fmt"
	"sync"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	stancesMergePerson      string
	stancesMergePersonsFile string
	stancesMergeRecallSim   float64
	stancesMergeParallel    int
	stancesMergeDryRun      bool
)

var stancesMergeCmd = &cobra.Command{
	Use:   "merge",
	Short: "Collapse near-duplicate stances per person via LLM verification",
	Long: `Per-person stance merge:
  - Few stances (<= the all-stances cap): hand them all to the LLM in one call.
  - Many stances: PAIRWISE — for each embedding-similar pair (>= --recall-threshold,
    default 0.82), ask the LLM whether they cover the same subject, union-find the
    confirmed pairs into focused groups, and fold each group directly. This avoids
    the recall collapse of one big all-stances call and the imprecision of
    embedding clustering.

--recall-threshold is the cosine floor for surfacing candidate PAIRS. It sits
above the empirical noise band (~0.80): genuine near-duplicate stances embed at
~0.86-0.90, while related-but-distinct pairs stay <=0.80 where the LLM gate is
unreliable. The gate still vetoes a wrong high-similarity pair.

Examples:
  ./dm stances merge --person "Marine Le Pen"
  ./dm stances merge --persons-file /tmp/heavies.txt --parallel 4
  ./dm stances merge                                       # all persons
  ./dm stances merge --recall-threshold 0.70 --dry-run`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runStancesMerge,
}

func init() {
	stancesCmd.AddCommand(stancesMergeCmd)
	stancesMergeCmd.Flags().StringVar(&stancesMergePerson, "person", "", "Specific person name to merge (mutually exclusive with --persons-file)")
	stancesMergeCmd.Flags().StringVar(&stancesMergePersonsFile, "persons-file", "", "Path to a file with one person name per line")
	stancesMergeCmd.Flags().Float64Var(&stancesMergeRecallSim, "recall-threshold", core.DefaultStanceMergeOptions.RecallSim, "Cosine-similarity floor for surfacing candidate pairs (the LLM gates each)")
	stancesMergeCmd.Flags().IntVar(&stancesMergeParallel, "parallel", 1, "Number of persons to merge concurrently")
	stancesMergeCmd.Flags().BoolVar(&stancesMergeDryRun, "dry-run", false, "Log proposed merges without writing to the database")
}

func runStancesMerge(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	// The stance manager is needed even in dry-run: for fragmented persons the
	// candidate groups are formed by asking the LLM per embedding-similar pair,
	// so a dry-run previews the actual LLM-gated groups. Only the final apply
	// (MergeClusterDecision + write) is skipped on dry-run.
	stanceManager, err := core.NewStanceManagerFromEnv(storer)
	if err != nil {
		return fmt.Errorf("failed to create stance manager: %w", err)
	}

	if stancesMergePerson != "" && stancesMergePersonsFile != "" {
		return fmt.Errorf("--person and --persons-file are mutually exclusive")
	}

	var persons []core.PersonRecord
	if stancesMergePersonsFile != "" {
		persons, err = resolvePersonsFromFile(ctx, storer, stancesMergePersonsFile)
	} else {
		persons, err = personsForCommand(ctx, storer, stancesMergePerson, storer.GetPersonsWithFacts)
	}
	if err != nil {
		return err
	}

	if stancesMergeParallel < 1 {
		stancesMergeParallel = 1
	}
	if stancesMergeRecallSim < 0 || stancesMergeRecallSim > 1 {
		return fmt.Errorf("--recall-threshold must be in [0,1]")
	}

	log.Info().
		Int("person_count", len(persons)).
		Int("parallel", stancesMergeParallel).
		Float64("recall_sim_threshold", stancesMergeRecallSim).
		Bool("dry_run", stancesMergeDryRun).
		Msg("Starting stance LLM-merge phase")

	var mu sync.Mutex
	var stats core.StanceMergeSweepStats

	processOne := func(p core.PersonRecord) {
		ps, err := core.MergeStancesForPerson(ctx, storer, stanceManager, p, core.StanceMergeOptions{
			RecallSim: stancesMergeRecallSim,
			DryRun:    stancesMergeDryRun,
		})
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("LLM-merge failed for person")
			return
		}
		for _, cluster := range ps.DryRunClusters {
			fmt.Printf("[DRY-RUN] %s: candidate cluster of %d stances:\n", p.Name, len(cluster))
			for _, s := range cluster {
				fmt.Printf("    id=%d label=%q\n", s.ID, s.Label)
			}
		}
		mu.Lock()
		stats.Add(ps)
		mu.Unlock()
	}

	jobs := make(chan core.PersonRecord)
	var wg sync.WaitGroup
	for i := 0; i < stancesMergeParallel; i++ {
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

	rows := []resultRow{
		{Label: "Persons processed", Value: stats.PersonsProcessed},
		{Label: "Persons skipped (<2 stances)", Value: stats.PersonsSkipped},
		{Label: "Candidate clusters (Phase A)", Value: stats.CandidateClusters},
		{Label: "LLM calls (Phase B)", Value: stats.LLMCalls},
		{Label: "LLM failures", Value: stats.LLMFailures},
		{Label: "Merge subgroups applied", Value: stats.SubgroupsApplied},
		{Label: "Stances merged away (src→dst)", Value: stats.StancesMerged},
		{Label: "Post-merge contradictions found", Value: stats.PostMergeContradictions},
	}
	if stats.PostMergeAnalyzeFailures > 0 {
		rows = append(rows, resultRow{Label: "Post-merge analyze failures", Value: stats.PostMergeAnalyzeFailures})
	}
	printResults("STANCE LLM-MERGE RESULTS", rows)
	if stancesMergeDryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}
	return nil
}
