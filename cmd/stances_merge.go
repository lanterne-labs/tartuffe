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
	stancesMergeCmd.Flags().Float64Var(&stancesMergeRecallSim, "recall-threshold", defaultMergeOptions.RecallSim, "Cosine-similarity floor for surfacing candidate pairs (the LLM gates each)")
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

	// The stance manager is needed even in dry-run: for fragmented persons the
	// candidate groups are formed by asking the LLM per embedding-similar pair
	// (pairwiseGroupStances), so a dry-run previews the actual LLM-gated groups.
	// Only the final apply (MergeClusterDecision + write) is skipped on dry-run.
	stanceManager, err := core.NewStanceManagerFromEnv(adapter)
	if err != nil {
		return fmt.Errorf("failed to create stance manager: %w", err)
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

	log.Info().
		Int("person_count", len(persons)).
		Int("parallel", stancesMergeParallel).
		Float64("recall_sim_threshold", stancesMergeRecallSim).
		Bool("dry_run", stancesMergeDryRun).
		Msg("Starting stance LLM-merge phase")

	var stats mergeStats

	processOne := func(p storage.PersonInfo) {
		ps, err := mergeStancesForPersonViaLLM(ctx, storer, stanceManager, p, mergeOptions{
			RecallSim: stancesMergeRecallSim,
			DryRun:    stancesMergeDryRun,
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
	RecallSim float64 // cosine floor for surfacing candidate pairs (the LLM gates each)
	DryRun    bool
}

// defaultMergeOptions is the single source of truth for merge thresholds used
// by the daily pipeline (runStancesMergeStep), the standalone CLI flags
// (`dm stances merge`), and the per-person rebuild merge inside
// backfillStancesForPerson. Changing these defaults in one place propagates
// everywhere.
var defaultMergeOptions = mergeOptions{
	// 0.82 sits ABOVE the empirical noise band. Verified on prod (2026-06-15,
	// Éric Zemmour): genuine near-duplicate stances embed at ~0.86-0.90 while
	// merely-related-but-distinct pairs (immigration↔identité, parcours↔identité,
	// droite2027↔parcours) all sit at <=0.80. The LLM pair-gate is NOT reliable in
	// the 0.65-0.80 band — it confirmed immigration↔identité at 0.68 — so a loose
	// floor lets union-find chain a fuzzy supercluster. Keeping candidates at
	// >=0.82 means the gate only ever sees genuine near-dups.
	RecallSim: 0.82,
	DryRun:    false,
}

// allStancesLLMCap is the per-person stance count at or below which we skip
// Phase-A clustering and hand every stance to the merge LLM in one call.
//
// Kept small on purpose: the merge LLM's RECALL collapses when it has to
// enumerate every mergeable subgroup among many stances at once. Verified in
// prod (2026-06-14): a single call over Éric Ciotti's 30 stances — which
// included FIVE "sécurité publique…" stances and THREE Olympics stances —
// returned ZERO merges. So a person with more than a handful of stances is
// routed through PAIRWISE grouping (pairwiseGroupStances): each embedding-similar
// pair (>= RecallSim) is a focused 2-stance yes/no question to the LLM, where its
// recall is reliable, and confirmed pairs are union-found into groups. The
// previous cap of 30 traded one edge case for a total recall failure on the
// most-fragmented people — the ones who need merging most. See mirror-86l.
const allStancesLLMCap = 8

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

	// Under Framing A (stance = neutral subject area, both pro and anti facts
	// belong inside the same stance), the LLM merge prompt's job is exactly to
	// recognise that two "directionally-framed" stances cover the same neutral
	// subject. Centroid similarity is a poor proxy for this: opposing-direction
	// facts have distant embeddings even when they share a subject, so Phase-A
	// clustering can filter out exactly the merges we need. Skip clustering
	// entirely for typical persons and hand all stances to the LLM in one call.
	//
	// Above the cap (fragmented politicians), the single call's recall collapses,
	// so we PAIRWISE-group instead: ask the LLM per embedding-similar pair and
	// union-find the confirmed ones into focused groups, each then handed back to
	// the merge LLM below for the actual label/summary + apply.
	// preConfirmed distinguishes the two regimes' apply semantics:
	//   - small person (<= cap): one cluster of ALL stances; the LLM still has to
	//     decide which SUBSETS merge, so we ask MergeClusterDecision per cluster.
	//   - fragmented person (> cap): pairwiseGroupStances already asked the LLM,
	//     per embedding-similar pair, whether each merges — so every group it
	//     returns is pre-confirmed and is folded DIRECTLY. Re-asking the LLM over
	//     the whole group reintroduces the recall collapse pairwise exists to
	//     avoid (verified 2026-06-15: a re-decision over Zemmour's confirmed
	//     immigration cluster merged nothing).
	var candidateClusters [][]int
	preConfirmed := false
	if len(docs) <= allStancesLLMCap {
		allIdx := make([]int, len(docs))
		for i := range docs {
			allIdx[i] = i
		}
		candidateClusters = [][]int{allIdx}
	} else {
		var pwCalls, pwFails int
		candidateClusters, pwCalls, pwFails = pairwiseGroupStances(ctx, stanceManager, p.Name, docs, opts.RecallSim)
		ps.llmCalls += int64(pwCalls)
		ps.llmFailures += int64(pwFails)
		preConfirmed = true
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

		var subgroups []core.MergeSubgroup
		if preConfirmed {
			// Every pair in this group was individually confirmed by the gate;
			// fold them all into one stance. applyMergeSubgroup regenerates the
			// unified label/summary from the merged fact set, so we don't supply
			// one here.
			ids := make([]int64, len(live))
			for i, s := range live {
				ids[i] = s.ID
			}
			subgroups = []core.MergeSubgroup{{StanceIDs: ids}}
		} else {
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
			subgroups = decision.Merges
		}

		for _, sub := range subgroups {
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

// pairwiseGroupStances groups a person's stances into merge-candidate clusters by
// asking the merge LLM, for each embedding-SIMILAR pair, whether the two cover the
// same subject (reusing MergeClusterDecision on a 2-stance cluster), then
// union-finding the confirmed pairs. This beats both alternatives for the
// fragmented-person case: unlike ONE big all-stances call (whose recall collapses
// — verified: 0 merges over 30 stances), each decision is a focused 2-stance
// question; unlike pure embedding clustering (a poor proxy for "same subject" — it
// pairs "sécurité publique" with "relations publiques"), a wrong pair the embedding
// surfaces is rejected per-pair and can't drag a whole cluster along. Returns
// candidate groups (size >= 2) as input indices, plus the LLM call/failure counts.
func pairwiseGroupStances(ctx context.Context, sm *core.StanceManager, personName string, docs []storage.StanceDocument, candidateSim float64) (groups [][]int, llmCalls, llmFailures int) {
	embeddings := make([][]float32, len(docs))
	for i, d := range docs {
		embeddings[i] = d.ClusterEmbedding
	}
	confirm := func(i, j int) bool {
		dec, err := sm.MergeClusterDecision(ctx, personName, []core.StanceForMerge{
			{ID: docs[i].ID, Label: docs[i].ClusterLabel, Summary: docs[i].Summary},
			{ID: docs[j].ID, Label: docs[j].ClusterLabel, Summary: docs[j].Summary},
		})
		llmCalls++
		if err != nil {
			llmFailures++
			return false
		}
		return dec != nil && len(dec.Merges) > 0
	}
	return groupByConfirmedSimilarPairs(embeddings, candidateSim, confirm), llmCalls, llmFailures
}

// groupByConfirmedSimilarPairs is the pure grouping core of pairwiseGroupStances:
// it forms candidate index pairs whose (L2-normalised) embeddings are at least
// candidateSim similar, asks confirm() for each — most-similar first, skipping
// pairs already linked transitively — and union-finds the confirmed pairs into
// groups of size >= 2 (sorted by size desc, then first index). Separated from the
// LLM call so it can be unit-tested with a fake predicate.
func groupByConfirmedSimilarPairs(embeddings [][]float32, candidateSim float64, confirm func(i, j int) bool) [][]int {
	n := len(embeddings)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}

	type cand struct {
		i, j int
		sim  float64
	}
	var cands []cand
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if sim := dotProductF32(embeddings[i], embeddings[j]); sim >= candidateSim {
				cands = append(cands, cand{i, j, sim})
			}
		}
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].sim > cands[b].sim })

	for _, c := range cands {
		if find(c.i) == find(c.j) {
			continue
		}
		if confirm(c.i, c.j) {
			parent[find(c.i)] = find(c.j)
		}
	}

	byRoot := map[int][]int{}
	for i := 0; i < n; i++ {
		byRoot[find(i)] = append(byRoot[find(i)], i)
	}
	var groups [][]int
	for _, m := range byRoot {
		if len(m) >= 2 {
			sort.Ints(m)
			groups = append(groups, m)
		}
	}
	sort.SliceStable(groups, func(a, b int) bool {
		if len(groups[a]) != len(groups[b]) {
			return len(groups[a]) > len(groups[b])
		}
		return groups[a][0] < groups[b][0]
	})
	return groups
}

// applyMergeSubgroup picks the destination stance (largest fact_count, ties by
// smallest ID), recomputes the weighted-normalised centroid across all
// members, calls MergeStancesForPerson once per src, then re-runs contradiction
// analysis on each absorbed fact (the previously-fragmented facts that just
// became co-located in the destination stance), and finally regenerates the
// destination's label + summary from the now-merged fact set.
//
// Returns:
//
//	merged         — number of source stances successfully folded into dst.
//	contradictions — number of new contradictions recorded by the post-merge
//	                 re-analysis (these are cross-stance pairs that the
//	                 per-fact assignTopic missed by splitting them).
//	analyzeFailures — non-fatal AnalyzeFactInStance errors (per absorbed fact).
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

	// Bring label + summary up to date with the merged fact set: a full rebuild
	// when the facts fit the context, otherwise the absorbed facts (added to dst
	// with a fresh added_at) are folded into dst's current summary. Supersedes
	// the LLM's MergeClusterDecision label/summary, which only saw the two
	// summaries.
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
