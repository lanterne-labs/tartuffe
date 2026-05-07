package cmd

import (
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"

	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	stancesMergePerson         string
	stancesMergePersonsFile    string
	stancesMergeRecallSim      float64
	stancesMergeMaxCluster     int
	stancesMergeParallel       int
	stancesMergeDryRun         bool
	stancesMergeSubclusterSim  float64
)

var stancesMergeCmd = &cobra.Command{
	Use:   "merge",
	Short: "Collapse near-duplicate stances per person via LLM verification",
	Long: `Two-phase per-person merge:
  Phase A — agglomerative average-linkage clustering on stance centroids
            (cosine), cut at --recall-threshold (default 0.65 sim / 0.35 dist).
  Phase B — for each multi-stance candidate cluster, ask an LLM whether subsets
            should actually merge; for each merge subgroup the LLM proposes,
            fold all srcs into the largest dst with a unified summary/label.

The Phase-A threshold is intentionally LOOSE — false positives are filtered by
the LLM in Phase B. Lowering it costs only LLM calls; raising it risks missing
obvious merges.

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
	stancesMergeCmd.Flags().Float64Var(&stancesMergeRecallSim, "recall-threshold", 0.65, "Cosine-similarity floor for Phase-A candidate clustering (default 0.65)")
	stancesMergeCmd.Flags().Float64Var(&stancesMergeSubclusterSim, "subcluster-threshold", 0.75, "Cosine-similarity floor for the sub-clustering pass when a Phase-A cluster exceeds --max-cluster")
	stancesMergeCmd.Flags().IntVar(&stancesMergeMaxCluster, "max-cluster", 30, "Phase-A cluster size cap before the sub-clustering pass kicks in")
	stancesMergeCmd.Flags().IntVar(&stancesMergeParallel, "parallel", 1, "Number of persons to merge concurrently")
	stancesMergeCmd.Flags().BoolVar(&stancesMergeDryRun, "dry-run", false, "Log proposed merges without writing to the database")
}

// mergeStats are the per-run aggregated counters printed at the end.
type mergeStats struct {
	PersonsProcessed   int64
	PersonsSkipped     int64
	CandidateClusters  int64
	LLMCalls           int64
	LLMFailures        int64
	StancesMerged      int64 // number of src stances folded away
	SubgroupsApplied   int64 // number of merge subgroups actually applied
}

func runStancesMerge(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	adapter := storage.NewStanceStorerAdapter(storer)

	var stanceManager *core.StanceManager
	if !stancesMergeDryRun {
		stanceManager, err = core.NewStanceManagerFromEnv(adapter)
		if err != nil {
			return fmt.Errorf("failed to create stance manager: %w", err)
		}
	}

	if stancesMergePerson != "" && stancesMergePersonsFile != "" {
		return fmt.Errorf("--person and --persons-file are mutually exclusive")
	}

	var persons []storage.PersonInfo
	if stancesMergePersonsFile != "" {
		persons, err = resolvePersonsFromFile(ctx, storer, stancesMergePersonsFile)
	} else {
		persons, err = getPersonsForBackfill(ctx, storer, stancesMergePerson)
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
	if stancesMergeSubclusterSim < stancesMergeRecallSim {
		return fmt.Errorf("--subcluster-threshold (%.3f) must be >= --recall-threshold (%.3f)", stancesMergeSubclusterSim, stancesMergeRecallSim)
	}
	if stancesMergeMaxCluster < 2 {
		stancesMergeMaxCluster = 2
	}

	log.Info().
		Int("person_count", len(persons)).
		Int("parallel", stancesMergeParallel).
		Float64("recall_sim_threshold", stancesMergeRecallSim).
		Float64("subcluster_sim_threshold", stancesMergeSubclusterSim).
		Int("max_cluster", stancesMergeMaxCluster).
		Bool("dry_run", stancesMergeDryRun).
		Msg("Starting stance LLM-merge phase")

	var stats mergeStats

	processOne := func(p storage.PersonInfo) {
		ps, err := mergeStancesForPersonViaLLM(ctx, storer, stanceManager, p, mergeOptions{
			RecallSim:        stancesMergeRecallSim,
			SubclusterSim:    stancesMergeSubclusterSim,
			MaxCluster:       stancesMergeMaxCluster,
			DryRun:           stancesMergeDryRun,
		})
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("LLM-merge failed for person")
			return
		}
		atomic.AddInt64(&stats.PersonsProcessed, 1)
		atomic.AddInt64(&stats.CandidateClusters, ps.candidateClusters)
		atomic.AddInt64(&stats.LLMCalls, ps.llmCalls)
		atomic.AddInt64(&stats.LLMFailures, ps.llmFailures)
		atomic.AddInt64(&stats.StancesMerged, ps.stancesMerged)
		atomic.AddInt64(&stats.SubgroupsApplied, ps.subgroupsApplied)
		if ps.skipped {
			atomic.AddInt64(&stats.PersonsSkipped, 1)
		}
	}

	jobs := make(chan storage.PersonInfo)
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

	fmt.Println()
	fmt.Println("=== STANCE LLM-MERGE RESULTS ===")
	fmt.Printf("Persons processed:               %d\n", stats.PersonsProcessed)
	fmt.Printf("Persons skipped (<2 stances):    %d\n", stats.PersonsSkipped)
	fmt.Printf("Candidate clusters (Phase A):    %d\n", stats.CandidateClusters)
	fmt.Printf("LLM calls (Phase B):             %d\n", stats.LLMCalls)
	fmt.Printf("LLM failures:                    %d\n", stats.LLMFailures)
	fmt.Printf("Merge subgroups applied:         %d\n", stats.SubgroupsApplied)
	fmt.Printf("Stances merged away (src→dst):   %d\n", stats.StancesMerged)
	if stancesMergeDryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}
	return nil
}

type mergeOptions struct {
	RecallSim     float64 // similarity floor → distance cutoff = 1 - RecallSim
	SubclusterSim float64 // tighter sim floor for sub-clustering oversize clusters
	MaxCluster    int     // size cap before sub-clustering kicks in
	DryRun        bool
}

type personMergeStats struct {
	skipped           bool
	candidateClusters int64
	llmCalls          int64
	llmFailures       int64
	stancesMerged     int64
	subgroupsApplied  int64
}

// mergeStancesForPersonViaLLM runs Phase A clustering + Phase B LLM verification
// for a single person's stances and applies the resulting merges through the
// storer. Errors from the LLM are logged and counted; they don't abort the
// person — the worst case is "this person didn't get merged this round".
func mergeStancesForPersonViaLLM(
	ctx context.Context,
	storer *storage.PostgresStorer,
	stanceManager *core.StanceManager,
	p storage.PersonInfo,
	opts mergeOptions,
) (personMergeStats, error) {
	var ps personMergeStats

	docs, err := storer.GetStanceDocumentsForPerson(ctx, p.ID)
	if err != nil {
		return ps, fmt.Errorf("loading stances: %w", err)
	}
	if len(docs) < 2 {
		ps.skipped = true
		return ps, nil
	}

	// Phase A: cluster centroids.
	centroids := make([][]float32, len(docs))
	for i, d := range docs {
		centroids[i] = d.ClusterEmbedding
	}
	distanceCutoff := 1.0 - opts.RecallSim
	clusters := agglomerativeAverageLinkage(centroids, distanceCutoff)

	// Drop singletons + sub-cluster anything oversized.
	var candidateClusters [][]int
	for _, cl := range clusters {
		if len(cl) < 2 {
			continue
		}
		if len(cl) > opts.MaxCluster {
			subs := subClusterIfNeeded(centroids, cl, 1.0-opts.SubclusterSim, opts.MaxCluster)
			candidateClusters = append(candidateClusters, subs...)
			continue
		}
		candidateClusters = append(candidateClusters, cl)
	}

	if len(candidateClusters) == 0 {
		return ps, nil
	}
	ps.candidateClusters = int64(len(candidateClusters))

	for _, cluster := range candidateClusters {
		// Re-load every iteration: previous merges may have removed/altered IDs.
		current, err := storer.GetStanceDocumentsForPerson(ctx, p.ID)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Re-loading stances mid-merge failed; aborting person")
			return ps, err
		}
		byID := make(map[int64]storage.StanceDocument, len(current))
		for _, d := range current {
			byID[d.ID] = d
		}

		live := make([]core.StanceForMerge, 0, len(cluster))
		for _, idx := range cluster {
			origID := docs[idx].ID
			d, ok := byID[origID]
			if !ok {
				// Stance was already merged by a previous subgroup in this run.
				continue
			}
			live = append(live, core.StanceForMerge{
				ID:      d.ID,
				Label:   d.ClusterLabel,
				Summary: d.Summary,
			})
		}
		if len(live) < 2 {
			continue
		}

		if opts.DryRun {
			fmt.Printf("[DRY-RUN] %s: candidate cluster of %d stances:\n", p.Name, len(live))
			for _, s := range live {
				fmt.Printf("    id=%d label=%q\n", s.ID, s.Label)
			}
			continue
		}

		ps.llmCalls++
		decision, err := stanceManager.MergeClusterDecision(ctx, p.Name, live)
		if err != nil {
			log.Warn().Err(err).Str("person", p.Name).Int("cluster_size", len(live)).Msg("LLM merge decision failed; cluster left as-is")
			ps.llmFailures++
			continue
		}
		if decision == nil || len(decision.Merges) == 0 {
			continue
		}

		for _, sub := range decision.Merges {
			merged, err := applyMergeSubgroup(ctx, storer, byID, sub)
			if err != nil {
				log.Warn().Err(err).Str("person", p.Name).Interface("subgroup", sub).Msg("Apply merge subgroup failed; skipped")
				continue
			}
			if merged > 0 {
				ps.subgroupsApplied++
				ps.stancesMerged += int64(merged)
			}
		}
	}

	log.Info().
		Str("person", p.Name).
		Int64("candidate_clusters", ps.candidateClusters).
		Int64("llm_calls", ps.llmCalls).
		Int64("llm_failures", ps.llmFailures).
		Int64("subgroups_applied", ps.subgroupsApplied).
		Int64("stances_merged", ps.stancesMerged).
		Msg("LLM-merge done for person")

	return ps, nil
}

// subClusterIfNeeded re-runs agglomerative clustering on the indices of a
// single oversized cluster at a stricter cutoff. If sub-clusters are still
// oversized, it logs a warning and falls back to chunking by fact_count.
func subClusterIfNeeded(centroids [][]float32, originalCluster []int, distanceCutoff float64, maxCluster int) [][]int {
	subCentroids := make([][]float32, len(originalCluster))
	for i, idx := range originalCluster {
		subCentroids[i] = centroids[idx]
	}
	subClusters := agglomerativeAverageLinkage(subCentroids, distanceCutoff)

	var out [][]int
	for _, sc := range subClusters {
		if len(sc) < 2 {
			continue
		}
		// Map sub-cluster local indices back to original-input indices.
		mapped := make([]int, len(sc))
		for i, localIdx := range sc {
			mapped[i] = originalCluster[localIdx]
		}
		if len(mapped) > maxCluster {
			log.Warn().
				Int("subcluster_size", len(mapped)).
				Int("max_cluster", maxCluster).
				Msg("Sub-cluster still exceeds --max-cluster; chunking by index order")
			for i := 0; i < len(mapped); i += maxCluster {
				end := i + maxCluster
				if end > len(mapped) {
					end = len(mapped)
				}
				if end-i < 2 {
					continue
				}
				out = append(out, mapped[i:end])
			}
			continue
		}
		out = append(out, mapped)
	}
	return out
}

// applyMergeSubgroup picks the destination stance (largest fact_count, ties by
// smallest ID), recomputes the weighted-normalised centroid across all
// members, calls MergeStancesForPerson once per src, then writes the LLM's
// unified summary + label to the dst. Returns the number of srcs successfully
// merged.
func applyMergeSubgroup(
	ctx context.Context,
	storer *storage.PostgresStorer,
	currentByID map[int64]storage.StanceDocument,
	sub core.MergeSubgroup,
) (int, error) {
	if len(sub.StanceIDs) < 2 {
		return 0, nil
	}

	// Resolve: drop IDs that no longer exist (already merged).
	live := make([]storage.StanceDocument, 0, len(sub.StanceIDs))
	for _, id := range sub.StanceIDs {
		if d, ok := currentByID[id]; ok {
			live = append(live, d)
		}
	}
	if len(live) < 2 {
		return 0, nil
	}

	// Pick dst: largest fact_count, ties by smallest ID.
	dstIdx := 0
	for i := 1; i < len(live); i++ {
		if live[i].FactCount > live[dstIdx].FactCount ||
			(live[i].FactCount == live[dstIdx].FactCount && live[i].ID < live[dstIdx].ID) {
			dstIdx = i
		}
	}
	dst := live[dstIdx]

	// Build the input slice for weightedNormalisedCentroid (reuses the helper
	// from cmd/stances_backfill.go). Indices of `inputs` correspond to `live`.
	inputs := make([]stanceMergeInput, len(live))
	indices := make([]int, len(live))
	for i, d := range live {
		inputs[i] = stanceMergeInput{ID: d.ID, FactCount: d.FactCount, Centroid: d.ClusterEmbedding}
		indices[i] = i
	}
	newCentroid := weightedNormalisedCentroid(inputs, indices)

	// Order srcs by ID for deterministic apply order.
	srcs := make([]storage.StanceDocument, 0, len(live)-1)
	for i, d := range live {
		if i == dstIdx {
			continue
		}
		srcs = append(srcs, d)
	}
	sort.Slice(srcs, func(i, j int) bool { return srcs[i].ID < srcs[j].ID })

	merged := 0
	for _, src := range srcs {
		if err := storer.MergeStancesForPerson(ctx, src.ID, dst.ID, newCentroid); err != nil {
			log.Warn().Err(err).Int64("src", src.ID).Int64("dst", dst.ID).Msg("MergeStancesForPerson failed; subgroup partial")
			continue
		}
		merged++
		// Reflect the merge in the local map so subsequent subgroups don't
		// reference a now-deleted src.
		delete(currentByID, src.ID)
	}

	if merged > 0 {
		// Write the unified summary + label to the dst once at the end.
		if err := storer.UpdateStanceSummary(ctx, dst.ID, sub.Summary, sub.Label); err != nil {
			log.Warn().Err(err).Int64("dst", dst.ID).Msg("UpdateStanceSummary failed (merge applied; dst keeps old summary/label)")
		} else {
			// Refresh the local view of dst to keep the in-memory map consistent.
			updated := dst
			updated.Summary = sub.Summary
			updated.ClusterLabel = sub.Label
			currentByID[dst.ID] = updated
		}
	}

	return merged, nil
}
