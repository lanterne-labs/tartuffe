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
	PersonsProcessed         int64
	PersonsSkipped           int64
	CandidateClusters        int64
	LLMCalls                 int64
	LLMFailures              int64
	StancesMerged            int64 // number of src stances folded away
	SubgroupsApplied         int64 // number of merge subgroups actually applied
	PostMergeContradictions  int64 // contradictions found between previously-fragmented facts
	PostMergeAnalyzeFailures int64 // per-fact analyze failures in the post-merge re-detection pass
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
		atomic.AddInt64(&stats.PostMergeContradictions, ps.postMergeContradictions)
		atomic.AddInt64(&stats.PostMergeAnalyzeFailures, ps.postMergeAnalyzeFailures)
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
	fmt.Printf("Post-merge contradictions found: %d\n", stats.PostMergeContradictions)
	if stats.PostMergeAnalyzeFailures > 0 {
		fmt.Printf("Post-merge analyze failures:     %d\n", stats.PostMergeAnalyzeFailures)
	}
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
	skipped                  bool
	candidateClusters        int64
	llmCalls                 int64
	llmFailures              int64
	stancesMerged            int64
	subgroupsApplied         int64
	postMergeContradictions  int64
	postMergeAnalyzeFailures int64
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
			merged, contradictions, analyzeFailures, err := applyMergeSubgroup(ctx, storer, stanceManager, p.ID, byID, sub)
			if err != nil {
				log.Warn().Err(err).Str("person", p.Name).Interface("subgroup", sub).Msg("Apply merge subgroup failed; skipped")
				continue
			}
			if merged > 0 {
				ps.subgroupsApplied++
				ps.stancesMerged += int64(merged)
			}
			ps.postMergeContradictions += int64(contradictions)
			ps.postMergeAnalyzeFailures += int64(analyzeFailures)
		}
	}

	log.Info().
		Str("person", p.Name).
		Int64("candidate_clusters", ps.candidateClusters).
		Int64("llm_calls", ps.llmCalls).
		Int64("llm_failures", ps.llmFailures).
		Int64("subgroups_applied", ps.subgroupsApplied).
		Int64("stances_merged", ps.stancesMerged).
		Int64("post_merge_contradictions", ps.postMergeContradictions).
		Int64("post_merge_analyze_failures", ps.postMergeAnalyzeFailures).
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
// members, calls MergeStancesForPerson once per src, then re-runs contradiction
// analysis on each absorbed fact (the previously-fragmented facts that just
// became co-located in the destination stance), and finally regenerates the
// destination's label + summary from the now-merged fact set.
//
// Returns:
//   merged         — number of source stances successfully folded into dst.
//   contradictions — number of new contradictions recorded by the post-merge
//                    re-analysis (these are cross-stance pairs that the
//                    per-fact assignTopic missed by splitting them).
//   analyzeFailures — non-fatal AnalyzeFactInStance errors (per absorbed fact).
func applyMergeSubgroup(
	ctx context.Context,
	storer *storage.PostgresStorer,
	stanceManager *core.StanceManager,
	personID int64,
	currentByID map[int64]storage.StanceDocument,
	sub core.MergeSubgroup,
) (merged int, contradictions int, analyzeFailures int, err error) {
	if len(sub.StanceIDs) < 2 {
		return 0, 0, 0, nil
	}

	// Resolve: drop IDs that no longer exist (already merged).
	live := make([]storage.StanceDocument, 0, len(sub.StanceIDs))
	for _, id := range sub.StanceIDs {
		if d, ok := currentByID[id]; ok {
			live = append(live, d)
		}
	}
	if len(live) < 2 {
		return 0, 0, 0, nil
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

	// Snapshot src facts BEFORE the merge so we know which facts are "absorbed"
	// (i.e., previously in a separate stance). After MergeStancesForPerson the
	// dst stance contains both its original facts and the absorbed ones, but we
	// need the IDs of the absorbed ones to re-analyze them against the dst's
	// other facts.
	absorbedFactIDs := make(map[int64]bool)
	for _, src := range srcs {
		facts, ferr := storer.GetFactsForStance(ctx, src.ID)
		if ferr != nil {
			log.Warn().Err(ferr).Int64("src", src.ID).Msg("Snapshot src facts failed; absorbed facts from this src won't be re-analyzed")
			continue
		}
		for _, f := range facts {
			absorbedFactIDs[f.ID] = true
		}
	}

	for _, src := range srcs {
		if mErr := storer.MergeStancesForPerson(ctx, src.ID, dst.ID, newCentroid); mErr != nil {
			log.Warn().Err(mErr).Int64("src", src.ID).Int64("dst", dst.ID).Msg("MergeStancesForPerson failed; subgroup partial")
			continue
		}
		merged++
		// Reflect the merge in the local map so subsequent subgroups don't
		// reference a now-deleted src.
		delete(currentByID, src.ID)
	}

	if merged == 0 {
		return 0, 0, 0, nil
	}

	// Refresh dst's in-memory view: centroid is updated by MergeStancesForPerson,
	// fact_count grows. Use the LLM's proposed summary as a placeholder; we
	// regenerate it via RefreshStanceMetadata at the end (which sees the actual
	// merged fact list and preserves any tensions the LLM didn't notice when
	// it only had label+summary inputs).
	updatedDst := dst
	updatedDst.Summary = sub.Summary
	updatedDst.ClusterLabel = sub.Label

	// Re-detect contradictions: for each absorbed fact, analyze it against the
	// merged stance's other facts. RecordContradiction is idempotent on
	// (fact_a_id, fact_b_id) so re-analyzing a fact whose pair was already
	// detected within its original stance is safe.
	allFacts, fErr := storer.GetFactsForStance(ctx, dst.ID)
	if fErr != nil {
		log.Warn().Err(fErr).Int64("dst", dst.ID).Msg("Loading merged-stance facts failed; skipping post-merge re-detection")
	} else {
		for absorbedID := range absorbedFactIDs {
			var thisFact core.StanceFactInfo
			var found bool
			others := make([]core.StanceFactInfo, 0, len(allFacts)-1)
			for _, f := range allFacts {
				cf := core.StanceFactInfo{
					ID:                 f.ID,
					NormalizedFactText: f.NormalizedFactText,
					EmbeddingText:      f.EmbeddingText,
					CanonicalVerb:      f.CanonicalVerb,
				}
				if f.ID == absorbedID {
					thisFact = cf
					found = true
					continue
				}
				others = append(others, cf)
			}
			if !found {
				// Absorbed fact isn't in dst — race or unrelated state. Skip.
				continue
			}
			coreDst := core.StanceDocument{
				ID:           updatedDst.ID,
				PersonID:     updatedDst.PersonID,
				ClusterLabel: updatedDst.ClusterLabel,
				Summary:      updatedDst.Summary,
			}
			n, aErr := stanceManager.AnalyzeFactInStance(ctx, personID, coreDst, thisFact, others)
			if aErr != nil {
				log.Warn().Err(aErr).Int64("dst", dst.ID).Int64("fact_id", absorbedID).Msg("Post-merge AnalyzeFactInStance failed")
				analyzeFailures++
				continue
			}
			contradictions += n
		}
	}

	// Regenerate label + summary from the now-merged fact set. Supersedes the
	// LLM's MergeClusterDecision label/summary because refresh sees ALL facts
	// including any contradictions just detected, and is instructed to preserve
	// tensions in the summary.
	if rErr := stanceManager.RefreshStanceMetadata(ctx, dst.ID); rErr != nil {
		log.Warn().Err(rErr).Int64("dst", dst.ID).Msg("RefreshStanceMetadata failed; dst keeps the merge-decision label/summary")
		// Fall back: persist the merge-decision label/summary so dst isn't left
		// stale (UpdateStanceSummary is the historical behavior).
		if uErr := storer.UpdateStanceSummary(ctx, dst.ID, sub.Summary, sub.Label); uErr != nil {
			log.Warn().Err(uErr).Int64("dst", dst.ID).Msg("UpdateStanceSummary fallback also failed")
		} else {
			currentByID[dst.ID] = updatedDst
		}
	} else {
		// Refresh succeeded; we don't know the new label/summary text, so
		// invalidate the local cache for dst (subsequent reads in this run will
		// re-fetch from DB if needed).
		updatedDst.Summary = ""
		updatedDst.ClusterLabel = ""
		currentByID[dst.ID] = updatedDst
	}

	return merged, contradictions, analyzeFailures, nil
}
