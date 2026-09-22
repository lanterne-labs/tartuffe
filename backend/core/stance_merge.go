package core

import (
	"context"
	"crypto/sha256"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/util"
	"encoding/hex"
	"fmt"
	"sort"
	"time"

	"github.com/rs/zerolog/log"
)

// StanceMergeStorer is the storage view the stance merge sweep needs.
// storage.PostgresStorer implements it.
type StanceMergeStorer interface {
	GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]StanceDocument, error)
	GetFactsForStance(ctx context.Context, stanceID int64) ([]StanceFactInfo, error)
	MergeStancesForPerson(ctx context.Context, srcID, dstID int64, newDstCentroid []float32) error
	UpdateStanceSummary(ctx context.Context, stanceID int64, summary, clusterLabel string) error
	// ListLabelSimilarStancePairs returns the person's stance pairs whose
	// labels have trigram similarity of at least floor, as [smaller id,
	// larger id].
	ListLabelSimilarStancePairs(ctx context.Context, personID int64, floor float64) ([][2]int64, error)
	// ListPersonsNeedingMergeSweep lists the persons with >= 2 stances whose
	// stance set changed since the sweep last judged it under rulebookKey, or
	// that were never judged under it.
	ListPersonsNeedingMergeSweep(ctx context.Context, rulebookKey string) ([]PersonRecord, error)
	// RecordMergeSweep memoizes that the person's current stance set was
	// judged under rulebookKey.
	RecordMergeSweep(ctx context.Context, personID int64, rulebookKey string) error
}

// StanceMergeOptions tunes the per-person merge and the sweep over persons.
type StanceMergeOptions struct {
	RecallSim float64 // cosine floor for surfacing candidate pairs (the LLM gates each)
	// LabelSim is the trigram floor on stance LABELS for a second candidate
	// source (0 = off). Two stances of one person that say the same thing in
	// their labels ("politique environnementale et régulations écologiques"
	// / "Politique environnementale et Pacte vert") had centroids at cosine
	// 0.44, far under RecallSim, because opposite-direction facts pull the
	// centroids apart. Label pairs are mostly shared generic words, though
	// ("relations bilatérales France-Hongrie" / "France-Espagne" scores
	// 0.64), so they pass a stricter same-subject gate before the merge
	// decision (see sameSubjectLabelPrompt).
	LabelSim float64
	DryRun   bool
	// MaxPersons caps how many candidate persons RunStanceMergeSweep processes
	// (0 = all), taken in the order storage lists them. Bounded smoke runs use it.
	MaxPersons int
}

// DefaultStanceMergeOptions is the single source of truth for merge thresholds
// used by the daily pipeline sweep, the standalone CLI flags (`dm stances
// merge`), and the per-person merge inside BackfillStancesForPerson. Changing
// these defaults in one place propagates everywhere.
var DefaultStanceMergeOptions = StanceMergeOptions{
	// 0.82 sits ABOVE the empirical noise band. Verified on prod (2026-06-15,
	// Éric Zemmour): genuine near-duplicate stances embed at ~0.86-0.90 while
	// merely-related-but-distinct pairs (immigration↔identité, parcours↔identité,
	// droite2027↔parcours) all sit at <=0.80. The LLM pair-gate is NOT reliable in
	// the 0.65-0.80 band — it confirmed immigration↔identité at 0.68 — so a loose
	// floor lets union-find chain a fuzzy supercluster. Keeping candidates at
	// >=0.82 means the gate only ever sees genuine near-dups.
	RecallSim: 0.82,
	// 0.40 reaches the known duplicates (0.43-0.77 on prod) at ~220 candidate
	// pairs over the whole table; the gate carries the precision.
	LabelSim: 0.40,
	DryRun:   false,
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

// PersonMergeStats are the counters of one person's merge pass.
type PersonMergeStats struct {
	Skipped                  bool // fewer than 2 stances
	CandidateClusters        int64
	LLMCalls                 int64
	LLMFailures              int64
	StancesMerged            int64 // number of src stances folded away
	SubgroupsApplied         int64 // number of merge subgroups actually applied
	PostMergeContradictions  int64 // contradictions found between previously-fragmented facts
	PostMergeAnalyzeFailures int64 // per-fact analyze failures in the post-merge re-detection pass
	// DryRunClusters holds, in dry-run mode only, the candidate clusters that
	// would have been handed to the merge step, for the caller to display.
	DryRunClusters [][]StanceForMerge
}

// StanceMergeSweepStats aggregates PersonMergeStats over a sweep.
type StanceMergeSweepStats struct {
	PersonsProcessed         int64
	PersonsSkipped           int64
	CandidateClusters        int64
	LLMCalls                 int64
	LLMFailures              int64
	StancesMerged            int64
	SubgroupsApplied         int64
	PostMergeContradictions  int64
	PostMergeAnalyzeFailures int64
}

// Add folds one person's counters into the sweep totals.
func (s *StanceMergeSweepStats) Add(ps PersonMergeStats) {
	s.PersonsProcessed++
	if ps.Skipped {
		s.PersonsSkipped++
	}
	s.CandidateClusters += ps.CandidateClusters
	s.LLMCalls += ps.LLMCalls
	s.LLMFailures += ps.LLMFailures
	s.StancesMerged += ps.StancesMerged
	s.SubgroupsApplied += ps.SubgroupsApplied
	s.PostMergeContradictions += ps.PostMergeContradictions
	s.PostMergeAnalyzeFailures += ps.PostMergeAnalyzeFailures
}

// MergeRulebookKey identifies the merge judge's prompt and the sweep's
// candidate floor: the memo of a person's sweep is keyed on it, so editing the
// prompt or the floor re-judges every person once.
func MergeRulebookKey(opts StanceMergeOptions) string {
	h := sha256.New()
	h.Write([]byte(mergeDecisionPrompt("\x00", []StanceForMerge{{ID: 0, Label: "\x00", Summary: "\x00"}})))
	h.Write([]byte(sameSubjectLabelPrompt("\x00", StanceForMerge{Label: "\x00", Summary: "\x00"}, StanceForMerge{Label: "\x00", Summary: "\x00"})))
	fmt.Fprintf(h, "|recall=%g|label=%g|cap=%d", opts.RecallSim, opts.LabelSim, allStancesLLMCap)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// RunStanceMergeSweep is the daily-pipeline stance merge step: for every person
// with at least two stances whose stance set changed since it was last judged
// (capped by opts.MaxPersons), LLM-vets the candidate clusters, applies the
// merges, and re-detects contradictions across the previously-fragmented
// facts. A person judged without LLM failures is memoized under the current
// stance fingerprint and rulebook (RecordMergeSweep), so unchanged persons
// cost the sweep nothing the next night. A per-person failure is logged and
// skipped, and stays pending.
func RunStanceMergeSweep(ctx context.Context, storer StanceMergeStorer, sm *StanceManager, opts StanceMergeOptions) (StanceMergeSweepStats, error) {
	var stats StanceMergeSweepStats
	start := time.Now()
	log.Info().Msg("Stance merge sweep started")

	key := MergeRulebookKey(opts)
	persons, err := storer.ListPersonsNeedingMergeSweep(ctx, key)
	if err != nil {
		return stats, fmt.Errorf("listing merge-candidate persons: %w", err)
	}
	pending := len(persons)
	if opts.MaxPersons > 0 && len(persons) > opts.MaxPersons {
		persons = persons[:opts.MaxPersons]
	}

	for _, p := range persons {
		ps, perr := MergeStancesForPerson(ctx, storer, sm, p, opts)
		if perr != nil {
			log.Warn().Err(perr).Str("person", p.Name).Msg("Merge sweep: per-person failure (continuing)")
			continue
		}
		stats.Add(ps)
		// A failed LLM call means a candidate went unjudged: leave the person
		// pending so the next sweep asks again. Dry runs judge without
		// applying, so their answer must not be memoized either.
		if ps.LLMFailures > 0 || opts.DryRun {
			continue
		}
		if err := storer.RecordMergeSweep(ctx, p.ID, key); err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Merge sweep: failed to record the sweep memo")
		}
	}

	log.Info().
		Dur("elapsed", time.Since(start)).
		Int("pending", pending).
		Int64("persons_processed", stats.PersonsProcessed).
		Int64("stances_merged", stats.StancesMerged).
		Int64("post_merge_contradictions", stats.PostMergeContradictions).
		Int64("post_merge_analyze_failures", stats.PostMergeAnalyzeFailures).
		Str("rulebook", key).
		Msg("Stance merge sweep finished")
	return stats, nil
}

// MergeStancesForPerson runs Phase A clustering + Phase B LLM verification
// for a single person's stances and applies the resulting merges through the
// storer. Errors from the LLM are logged and counted; they don't abort the
// person — the worst case is "this person didn't get merged this round".
func MergeStancesForPerson(
	ctx context.Context,
	storer StanceMergeStorer,
	stanceManager *StanceManager,
	p PersonRecord,
	opts StanceMergeOptions,
) (PersonMergeStats, error) {
	var ps PersonMergeStats

	docs, err := storer.GetStanceDocumentsForPerson(ctx, p.ID)
	if err != nil {
		return ps, fmt.Errorf("loading stances: %w", err)
	}
	if len(docs) < 2 {
		ps.Skipped = true
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
		var labelPairs [][2]int64
		if opts.LabelSim > 0 {
			labelPairs, err = storer.ListLabelSimilarStancePairs(ctx, p.ID, opts.LabelSim)
			if err != nil {
				return ps, fmt.Errorf("loading label-similar stance pairs: %w", err)
			}
		}
		var pwCalls, pwFails int
		candidateClusters, pwCalls, pwFails = pairwiseGroupStances(ctx, stanceManager, p.Name, docs, opts.RecallSim, labelPairs)
		ps.LLMCalls += int64(pwCalls)
		ps.LLMFailures += int64(pwFails)
		preConfirmed = true
	}

	if len(candidateClusters) == 0 {
		return ps, nil
	}
	ps.CandidateClusters = int64(len(candidateClusters))

	for _, cluster := range candidateClusters {
		// Re-load every iteration: previous merges may have removed/altered IDs.
		current, err := storer.GetStanceDocumentsForPerson(ctx, p.ID)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Re-loading stances mid-merge failed; aborting person")
			return ps, err
		}
		byID := make(map[int64]StanceDocument, len(current))
		for _, d := range current {
			byID[d.ID] = d
		}

		live := make([]StanceForMerge, 0, len(cluster))
		for _, idx := range cluster {
			origID := docs[idx].ID
			d, ok := byID[origID]
			if !ok {
				// Stance was already merged by a previous subgroup in this run.
				continue
			}
			live = append(live, StanceForMerge{
				ID:      d.ID,
				Label:   d.ClusterLabel,
				Summary: d.Summary,
			})
		}
		if len(live) < 2 {
			continue
		}

		if opts.DryRun {
			ps.DryRunClusters = append(ps.DryRunClusters, live)
			continue
		}

		var subgroups []MergeSubgroup
		if preConfirmed {
			// Every pair in this group was individually confirmed by the gate;
			// fold them all into one stance. applyMergeSubgroup regenerates the
			// unified label/summary from the merged fact set, so we don't supply
			// one here.
			ids := make([]int64, len(live))
			for i, s := range live {
				ids[i] = s.ID
			}
			subgroups = []MergeSubgroup{{StanceIDs: ids}}
		} else {
			ps.LLMCalls++
			decision, err := stanceManager.MergeClusterDecision(ctx, p.Name, live)
			if err != nil {
				log.Warn().Err(err).Str("person", p.Name).Int("cluster_size", len(live)).Msg("LLM merge decision failed; cluster left as-is")
				ps.LLMFailures++
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
				ps.SubgroupsApplied++
				ps.StancesMerged += int64(merged)
			}
			ps.PostMergeContradictions += int64(contradictions)
			ps.PostMergeAnalyzeFailures += int64(analyzeFailures)
		}
	}

	log.Info().
		Str("person", p.Name).
		Int64("candidate_clusters", ps.CandidateClusters).
		Int64("llm_calls", ps.LLMCalls).
		Int64("llm_failures", ps.LLMFailures).
		Int64("subgroups_applied", ps.SubgroupsApplied).
		Int64("stances_merged", ps.StancesMerged).
		Int64("post_merge_contradictions", ps.PostMergeContradictions).
		Int64("post_merge_analyze_failures", ps.PostMergeAnalyzeFailures).
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
// labelPairs are extra candidates by label similarity (stance ids); each is
// confirmed by the stricter same-subject gate first, then by the merge
// decision like an embedding candidate.
func pairwiseGroupStances(ctx context.Context, sm *StanceManager, personName string, docs []StanceDocument, candidateSim float64, labelPairs [][2]int64) (groups [][]int, llmCalls, llmFailures int) {
	embeddings := make([][]float32, len(docs))
	idx := make(map[int64]int, len(docs))
	for i, d := range docs {
		embeddings[i] = d.ClusterEmbedding
		idx[d.ID] = i
	}
	var extra [][2]int
	for _, pr := range labelPairs {
		i, okI := idx[pr[0]]
		j, okJ := idx[pr[1]]
		if okI && okJ {
			extra = append(extra, [2]int{i, j})
		}
	}
	forMerge := func(i int) StanceForMerge {
		return StanceForMerge{ID: docs[i].ID, Label: docs[i].ClusterLabel, Summary: docs[i].Summary}
	}
	confirm := func(i, j int, viaLabel bool) bool {
		rejected, confirmed := metrics.OutcomeMergeRejected, metrics.OutcomeMergeConfirmed
		if viaLabel {
			// Counted apart so the label candidate source can be judged on
			// what it actually contributes.
			rejected, confirmed = metrics.OutcomeMergeLabelRejected, metrics.OutcomeMergeViaLabel
			same, err := sm.SameSubjectByLabel(ctx, personName, forMerge(i), forMerge(j))
			llmCalls++
			if err != nil {
				llmFailures++
				metrics.RecordDecision(metrics.StageStanceMerge, metrics.OutcomeError)
				return false
			}
			if !same {
				metrics.RecordDecision(metrics.StageStanceMerge, rejected)
				return false
			}
		}
		dec, err := sm.MergeClusterDecision(ctx, personName, []StanceForMerge{forMerge(i), forMerge(j)})
		llmCalls++
		if err != nil {
			llmFailures++
			metrics.RecordDecision(metrics.StageStanceMerge, metrics.OutcomeError)
			return false
		}
		if dec != nil && len(dec.Merges) > 0 {
			metrics.RecordDecision(metrics.StageStanceMerge, confirmed)
			return true
		}
		metrics.RecordDecision(metrics.StageStanceMerge, rejected)
		return false
	}
	return groupByConfirmedSimilarPairs(embeddings, candidateSim, extra, confirm), llmCalls, llmFailures
}

// groupByConfirmedSimilarPairs is the pure grouping core of pairwiseGroupStances:
// it forms candidate index pairs whose (L2-normalised) embeddings are at least
// candidateSim similar, plus the extra pairs (label candidates, asked after
// the embedding ones and flagged viaLabel unless the embedding also
// surfaced them), asks confirm() for each — most-similar first, skipping pairs
// already linked transitively — and union-finds the confirmed pairs into
// groups of size >= 2 (sorted by size desc, then first index). Separated from
// the LLM call so it can be unit-tested with a fake predicate.
func groupByConfirmedSimilarPairs(embeddings [][]float32, candidateSim float64, extra [][2]int, confirm func(i, j int, viaLabel bool) bool) [][]int {
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
		i, j     int
		sim      float64
		viaLabel bool
	}
	var cands []cand
	seen := map[[2]int]bool{}
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if sim := util.DotProductF32(embeddings[i], embeddings[j]); sim >= candidateSim {
				cands = append(cands, cand{i: i, j: j, sim: sim})
				seen[[2]int{i, j}] = true
			}
		}
	}
	sort.Slice(cands, func(a, b int) bool { return cands[a].sim > cands[b].sim })
	for _, pr := range extra {
		i, j := pr[0], pr[1]
		if i > j {
			i, j = j, i
		}
		if i == j || i < 0 || j >= n || seen[[2]int{i, j}] {
			continue
		}
		seen[[2]int{i, j}] = true
		cands = append(cands, cand{i: i, j: j, viaLabel: true})
	}

	for _, c := range cands {
		if find(c.i) == find(c.j) {
			continue
		}
		if confirm(c.i, c.j, c.viaLabel) {
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
	storer StanceMergeStorer,
	stanceManager *StanceManager,
	personID int64,
	currentByID map[int64]StanceDocument,
	sub MergeSubgroup,
) (merged int, contradictions int, analyzeFailures int, err error) {
	if len(sub.StanceIDs) < 2 {
		return 0, 0, 0, nil
	}

	// Resolve: drop IDs that no longer exist (already merged).
	live := make([]StanceDocument, 0, len(sub.StanceIDs))
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

	// New centroid: fact_count-weighted mean of the members, L2-normalised.
	centroids := make([][]float32, len(live))
	weights := make([]int, len(live))
	for i, d := range live {
		centroids[i] = d.ClusterEmbedding
		weights[i] = d.FactCount
	}
	newCentroid := util.WeightedNormalisedCentroid(centroids, weights)

	// Order srcs by ID for deterministic apply order.
	srcs := make([]StanceDocument, 0, len(live)-1)
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
			var thisFact StanceFactInfo
			var found bool
			others := make([]StanceFactInfo, 0, len(allFacts)-1)
			for _, f := range allFacts {
				cf := StanceFactInfo{
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
			coreDst := StanceDocument{
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

// sameSubjectLabelPrompt is the strict gate for label-similar stance pairs.
// Rules first, the two stances next, response format last (prefix caching).
func sameSubjectLabelPrompt(personName string, a, b StanceForMerge) string {
	return fmt.Sprintf(`Two stance topics of the SAME person have similar-looking labels. Decide whether they are the SAME SUBJECT AREA — one topic recorded twice under two labels — so that one should absorb the other.

Label similarity is a weak hint: most such pairs only share a generic word. Answer true ONLY when a reader would call them one topic.

SAME subject (answer true):
- One label is a rewording or a sub-facet of the other on the same policy field, country, institution or affair: "politique du logement et rénovation" / "Politique du logement et aides à la rénovation"; "relations entre la France et l'Italie" / "relations franco-italiennes"; "lutte contre les discriminations" / "politique de lutte contre les discriminations".
- The summaries describe the same matter from the same angle, with the same actors and the same policy field.

DIFFERENT (answer false):
- A generic word in common on different fields: "réglementation des drones" / "réglementation des loyers"; "politique éducative" / "politique pénale"; "relations avec les médias" / "relations avec les syndicats".
- Different countries, institutions, parties or persons: "relations bilatérales France-Italie" / "relations bilatérales France-Portugal"; "présidence du parti" / "financement du parti".
- A field and one of its neighbours: "politique économique" / "politique sociale"; "sécurité publique" / "politique pénale"; "immigration" / "identité nationale" — related is not the same.
- A whole area and a specific affair or event inside it are two topics ("relations diplomatiques de la France" / "relations diplomatiques avec la Syrie").

Person: %s
TOPIC A: %q
  summary: %s
TOPIC B: %q
  summary: %s

Return ONLY strict JSON, nothing else:
{"same": true, "reasoning": "<one short sentence>"}
or
{"same": false, "reasoning": "<one short sentence>"}`, personName, a.Label, a.Summary, b.Label, b.Summary)
}

// SameSubjectByLabel asks the strict same-subject gate about two stances whose
// labels look alike. It is the first of two gates for label-surfaced merge
// candidates; the merge decision still has the last word.
func (sm *StanceManager) SameSubjectByLabel(ctx context.Context, personName string, a, b StanceForMerge) (bool, error) {
	content, err := sm.callJSONLLM(ctx, "You are a political stance topic classifier. Return only valid JSON.", sameSubjectLabelPrompt(personName, a, b), 0.0, 200, false)
	if err != nil {
		return false, fmt.Errorf("same-subject gate: %w", err)
	}
	verdict, err := llm.ParseJSONResponse[PairVerdict](content)
	if err != nil {
		return false, fmt.Errorf("same-subject gate: parsing response: %w", err)
	}
	return verdict.Same, nil
}
