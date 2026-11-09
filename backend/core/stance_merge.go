package core

import (
	"context"
	"crypto/sha256"
	"digital-mirror/backend/llm"
	"digital-mirror/backend/metrics"
	"digital-mirror/backend/util"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
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
	// GetStanceLookups returns the memoized lookups whose fingerprint is
	// still the given one (stance id -> named stance, 0 for none);
	// RecordStanceLookup memoizes one.
	GetStanceLookups(ctx context.Context, fingerprints map[int64]string) (map[int64]int64, error)
	RecordStanceLookup(ctx context.Context, stanceID int64, fingerprint string, namedID int64) error
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
	// LookupMaxFacts is the size at or below which a stance is looked up
	// against the person's other stances by the LLM (0 = off): a third
	// candidate source for the fragments the two floors above miss. Le Pen's
	// "guerre en Ukraine et diplomatie" (1 fact) beside "politique française
	// sur la guerre en Ukraine" (53) sat at cosine 0.55 and trigram 0.31. A
	// lookup names one candidate; the overlap gate confirms it.
	LookupMaxFacts int
	DryRun         bool
	// MaxPersons caps how many candidate persons RunStanceMergeSweep processes
	// (0 = all), taken in the order storage lists them. Bounded smoke runs use it.
	MaxPersons int
	// MaxLLMCalls is the number of LLM calls after which the sweep starts no
	// new person (0 = no cap); the persons left stay pending for the next
	// sweep. The maintenance pass uses it to stay short.
	MaxLLMCalls int64
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
	// 3 covers the fragments (51% of the stances hold one fact) without a
	// lookup for every grown stance.
	LookupMaxFacts: 3,
	DryRun:         false,
}

// lookupCandidatesCap bounds the stances listed to a lookup, the largest
// first: a fragment belongs to a grown subject.
const lookupCandidatesCap = 150

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
// mergeMaxConsecutiveFailures is how many LLM failures in a row stop a merge run.
const mergeMaxConsecutiveFailures = 3

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
	h.Write([]byte(sameSubjectLookupPrompt("\x00", StanceForMerge{Label: "\x00", Summary: "\x00"}, []StanceDocument{{ClusterLabel: "\x00"}})))
	h.Write([]byte(sameQuestionsPrompt("\x00", StanceForMerge{Label: "\x00"}, StanceForMerge{Label: "\x00"}, nil, nil)))
	fmt.Fprintf(h, "|recall=%g|label=%g|cap=%d|lookup=%d", opts.RecallSim, opts.LabelSim, allStancesLLMCap, opts.LookupMaxFacts)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// RunStanceMergeSweep is the daily-pipeline stance merge step: for every person
// with at least two stances whose stance set changed since it was last judged
// (capped by opts.MaxPersons and opts.MaxLLMCalls), LLM-vets the candidate clusters, applies the
// merges, and re-detects contradictions across the previously-fragmented
// facts. A person judged without LLM failures is memoized under the current
// stance fingerprint and rulebook (RecordMergeSweep), so unchanged persons
// cost the sweep nothing the next night. A per-person failure is logged and
// skipped, and stays pending. The sweep stops at the first call the LLM could
// not serve and returns an error wrapping llm.ErrUnavailable, and when ctx is
// cancelled with an error wrapping ctx.Err(), the person it was on left
// pending.
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

	// The persons are independent: they run as many at once as the endpoint
	// serves.
	var mu sync.Mutex
	err = llm.ForEach(ctx, persons, func(ctx context.Context, p PersonRecord) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stance merge sweep stopped: %w", err)
		}
		mu.Lock()
		over := opts.MaxLLMCalls > 0 && stats.LLMCalls >= opts.MaxLLMCalls
		mu.Unlock()
		if over {
			return nil
		}
		ps, perr := MergeStancesForPerson(ctx, storer, sm, p, opts)
		mu.Lock()
		stats.Add(ps)
		mu.Unlock()
		if errors.Is(perr, llm.ErrUnavailable) {
			return fmt.Errorf("stance merge sweep stopped at %s: %w", p.Name, perr)
		}
		if err := ctx.Err(); err != nil {
			// Cut under the person: candidates may have gone unjudged, so it
			// is not memoized and the next sweep asks again.
			return fmt.Errorf("stance merge sweep stopped at %s: %w", p.Name, err)
		}
		if perr != nil {
			log.Warn().Err(perr).Str("person", p.Name).Msg("Merge sweep: per-person failure (continuing)")
			return nil
		}
		// A failed LLM call means a candidate went unjudged: leave the person
		// pending so the next sweep asks again. Dry runs judge without
		// applying, so their answer must not be memoized either.
		if ps.LLMFailures > 0 || opts.DryRun {
			return nil
		}
		if err := storer.RecordMergeSweep(ctx, p.ID, key); err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Merge sweep: failed to record the sweep memo")
		}
		return nil
	})
	if err != nil {
		return stats, err
	}

	log.Info().
		Dur("elapsed", time.Since(start)).
		Int("pending", pending).
		Int64("persons_processed", stats.PersonsProcessed).
		Int64("llm_calls", stats.LLMCalls).
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
		var lookupPairs [][2]int64
		if opts.LookupMaxFacts > 0 {
			var luCalls, luFails int
			lookupPairs, luCalls, luFails, err = lookupFragments(ctx, storer, stanceManager, p.Name, docs, opts, !opts.DryRun)
			ps.LLMCalls += int64(luCalls)
			ps.LLMFailures += int64(luFails)
			if err != nil {
				return ps, err
			}
		}
		factsOf := func(stanceID int64) []string {
			facts, err := storer.GetFactsForStance(ctx, stanceID)
			if err != nil {
				log.Warn().Err(err).Int64("stance_id", stanceID).Msg("Loading the facts of a stance for the overlap gate failed; the gate sees the label only")
				return nil
			}
			out := make([]string, 0, overlapGateFacts)
			for _, f := range facts[:min(len(facts), overlapGateFacts)] {
				out = append(out, f.NormalizedFactText)
			}
			return out
		}
		var pwCalls, pwFails int
		candidateClusters, pwCalls, pwFails, err = pairwiseGroupStances(ctx, stanceManager, p.Name, docs, opts.RecallSim, labelPairs, lookupPairs, factsOf)
		ps.LLMCalls += int64(pwCalls)
		ps.LLMFailures += int64(pwFails)
		if err != nil {
			return ps, err
		}
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
			if errors.Is(err, llm.ErrUnavailable) {
				return ps, err
			}
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
			if merged > 0 {
				ps.SubgroupsApplied++
				ps.StancesMerged += int64(merged)
			}
			ps.PostMergeContradictions += int64(contradictions)
			ps.PostMergeAnalyzeFailures += int64(analyzeFailures)
			if errors.Is(err, llm.ErrUnavailable) || ctx.Err() != nil {
				return ps, err
			}
			if err != nil {
				// The subgroup is left unmerged; counted as a failure so the
				// person is not memoized and the next sweep proposes it again.
				log.Warn().Err(err).Str("person", p.Name).Interface("subgroup", sub).Msg("Apply merge subgroup failed; skipped")
				ps.LLMFailures++
				continue
			}
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
// When the LLM is unavailable no further pair is asked and the error (wrapping
// llm.ErrUnavailable) is returned.
// labelPairs are extra candidates by label similarity (stance ids); each is
// confirmed by the stricter same-subject gate first, then by the merge
// decision like an embedding candidate. lookupPairs are [small, named]
// pairs of lookupFragments; each is confirmed by the overlap gate alone
// (SameQuestions), which reads some facts of both stances (factsOf).
// Measured on Le Pen and Faure (2026-10-19): the merge decision alone folded
// wide neighbours ("campagne 2027" absorbing "financement des partis", 5 of
// 20 groups); the label gate refused a case inside its subject ("fiscalité
// du tabac" / "fiscalité"); a fragment gate on labels chained five countries
// into "politique étrangère" (6 of 11 right for Faure).
func pairwiseGroupStances(ctx context.Context, sm *StanceManager, personName string, docs []StanceDocument, candidateSim float64, labelPairs, lookupPairs [][2]int64, factsOf func(stanceID int64) []string) (groups [][]int, llmCalls, llmFailures int, err error) {
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
	// named maps an index pair, smaller first, to the [small, named] indexes
	// of its lookup.
	named := map[[2]int][2]int{}
	for _, pr := range lookupPairs {
		s, okS := idx[pr[0]]
		n, okN := idx[pr[1]]
		if !okS || !okN || s == n {
			continue
		}
		key := [2]int{min(s, n), max(s, n)}
		named[key] = [2]int{s, n}
		extra = append(extra, key)
	}
	forMerge := func(i int) StanceForMerge {
		return StanceForMerge{ID: docs[i].ID, Label: docs[i].ClusterLabel, Summary: docs[i].Summary}
	}
	var unavailable error
	consecutive := 0 // LLM failures in a row
	failed := func(err error) bool {
		llmFailures++
		consecutive++
		metrics.RecordDecision(metrics.StageStanceMerge, metrics.OutcomeError)
		if errors.Is(err, llm.ErrUnavailable) {
			unavailable = err
		} else if consecutive >= mergeMaxConsecutiveFailures {
			// An endpoint that fails every call without saying it is down
			// (2026-11-09: an account without credit answered 400 to every
			// call) must not get the whole candidate list: the merge stops.
			unavailable = fmt.Errorf("stance merge: %d LLM failures in a row, the merge stops: %w", consecutive, err)
		}
		return false
	}
	confirm := func(i, j int, viaLabel bool) bool {
		if unavailable != nil {
			return false
		}
		if pr, ok := named[[2]int{min(i, j), max(i, j)}]; ok {
			a, b := forMerge(pr[0]), forMerge(pr[1])
			same, err := sm.SameQuestions(ctx, personName, a, b, factsOf(a.ID), factsOf(b.ID))
			llmCalls++
			if err != nil {
				return failed(err)
			}
			consecutive = 0
			if !same {
				metrics.RecordDecision(metrics.StageStanceMerge, metrics.OutcomeMergeLookupRejected)
				return false
			}
			metrics.RecordDecision(metrics.StageStanceMerge, metrics.OutcomeMergeViaLookup)
			return true
		}
		rejected, confirmed := metrics.OutcomeMergeRejected, metrics.OutcomeMergeConfirmed
		if viaLabel {
			// Counted apart so the label candidate source can be judged on
			// what it actually contributes.
			rejected, confirmed = metrics.OutcomeMergeLabelRejected, metrics.OutcomeMergeViaLabel
			same, err := sm.SameSubjectByLabel(ctx, personName, forMerge(i), forMerge(j))
			llmCalls++
			if err != nil {
				return failed(err)
			}
			consecutive = 0
			if !same {
				metrics.RecordDecision(metrics.StageStanceMerge, rejected)
				return false
			}
		}
		dec, err := sm.MergeClusterDecision(ctx, personName, []StanceForMerge{forMerge(i), forMerge(j)})
		llmCalls++
		if err != nil {
			return failed(err)
		}
		consecutive = 0
		if dec != nil && len(dec.Merges) > 0 {
			metrics.RecordDecision(metrics.StageStanceMerge, confirmed)
			return true
		}
		metrics.RecordDecision(metrics.StageStanceMerge, rejected)
		return false
	}
	groups = groupByConfirmedSimilarPairs(embeddings, candidateSim, extra, confirm)
	if unavailable != nil {
		return nil, llmCalls, llmFailures, unavailable
	}
	return groups, llmCalls, llmFailures, nil
}

// groupByConfirmedSimilarPairs is the pure grouping core of pairwiseGroupStances:
// it forms candidate index pairs whose (L2-normalised) embeddings are at least
// candidateSim similar, plus the extra pairs (label and lookup candidates,
// asked after the embedding ones and flagged viaLabel unless the embedding
// also surfaced them), asks confirm() for each — most-similar first, skipping
// pairs already linked transitively — and union-finds the confirmed pairs into
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
// members, analyzes each fact of the source stances against the merged fact
// set (the previously-fragmented facts about to become co-located), then
// calls MergeStancesForPerson once per src, records the verified
// contradictions the analysis found, and finally regenerates the
// destination's label + summary from the now-merged fact set.
//
// The analysis runs BEFORE the merge commits: a merge deletes its source
// stances, after which nothing tells which facts still await their
// cross-stance analysis. When the LLM is unavailable (llm.ErrUnavailable) or
// ctx ends (the deadline of the phase), nothing is merged or recorded and the
// error is returned; the subgroup is proposed again by the next sweep. Any
// other analysis failure only skips that fact's cross-stance check. A label + summary refresh that fails on an
// unavailable LLM after the merge committed is returned too, with the
// counts of the committed merge. Contradictions are recorded only for the
// facts of the srcs whose merge committed, and only against facts now in dst.
//
// Returns:
//
//	merged          — number of source stances successfully folded into dst.
//	contradictions  — number of new contradictions recorded by the post-merge
//	                  re-analysis (these are cross-stance pairs that the
//	                  per-fact assignTopic missed by splitting them).
//	analyzeFailures — absorbed facts whose analysis failed (not for an
//	                  unreachable LLM) and were merged unchecked.
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

	// The merged fact set, dst's facts first, and the absorbed facts (those
	// of the srcs, previously in a separate stance). dstFacts are the facts
	// in dst once the merges below committed: a src whose merge fails keeps
	// its facts, and no pair involving them is recorded.
	allFacts, err := storer.GetFactsForStance(ctx, dst.ID)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("loading facts of dst stance %d: %w", dst.ID, err)
	}
	dstFacts := make(map[int64]bool, len(allFacts))
	for _, f := range allFacts {
		dstFacts[f.ID] = true
	}
	absorbedBySrc := make(map[int64][]StanceFactInfo, len(srcs))
	for _, src := range srcs {
		facts, ferr := storer.GetFactsForStance(ctx, src.ID)
		if ferr != nil {
			return 0, 0, 0, fmt.Errorf("loading facts of src stance %d: %w", src.ID, ferr)
		}
		absorbedBySrc[src.ID] = facts
		allFacts = append(allFacts, facts...)
	}

	// Re-detect contradictions: analyze each absorbed fact against the merged
	// stance's other facts. Recording is idempotent on (fact_a_id, fact_b_id),
	// so re-analyzing a pair already detected within its original stance is
	// safe. A fact whose analysis fails for another reason than an
	// unreachable LLM or a cut context is merged without its cross-stance
	// check (counted).
	analysisStance := StanceDocument{ID: dst.ID, PersonID: dst.PersonID, ClusterLabel: sub.Label, Summary: sub.Summary}
	found := make(map[int64][]StanceContradictionEntry)
	for _, src := range srcs {
		for _, fact := range absorbedBySrc[src.ID] {
			others := make([]StanceFactInfo, 0, len(allFacts)-1)
			for _, f := range allFacts {
				if f.ID != fact.ID {
					others = append(others, f)
				}
			}
			entries, aErr := stanceManager.verifiedContradictionsInStance(ctx, analysisStance, fact, others)
			if errors.Is(aErr, llm.ErrUnavailable) || ctx.Err() != nil {
				// An unreachable LLM, or the deadline of the phase: nothing is
				// merged, and the next sweep proposes the subgroup again.
				return 0, 0, 0, fmt.Errorf("post-merge analysis of fact %d: %w", fact.ID, aErr)
			}
			if aErr != nil {
				log.Warn().Err(aErr).Int64("dst", dst.ID).Int64("fact_id", fact.ID).Msg("Post-merge analysis failed; fact merged unchecked")
				analyzeFailures++
				continue
			}
			found[fact.ID] = entries
		}
	}

	var mergedSrcs []StanceDocument
	for _, src := range srcs {
		if mErr := storer.MergeStancesForPerson(ctx, src.ID, dst.ID, newCentroid); mErr != nil {
			log.Warn().Err(mErr).Int64("src", src.ID).Int64("dst", dst.ID).Msg("MergeStancesForPerson failed; subgroup partial")
			continue
		}
		merged++
		mergedSrcs = append(mergedSrcs, src)
		for _, f := range absorbedBySrc[src.ID] {
			dstFacts[f.ID] = true
		}
		// Reflect the merge in the local map so subsequent subgroups don't
		// reference a now-deleted src.
		delete(currentByID, src.ID)
	}

	if merged == 0 {
		return 0, 0, analyzeFailures, nil
	}

	for _, src := range mergedSrcs {
		for _, fact := range absorbedBySrc[src.ID] {
			var inDst []StanceContradictionEntry
			for _, e := range found[fact.ID] {
				if dstFacts[e.KeyFactID] {
					inDst = append(inDst, e)
				}
			}
			contradictions += stanceManager.recordStanceContradictions(ctx, personID, fact.ID, dst.ID, inDst)
		}
	}

	// Refresh dst's in-memory view: centroid is updated by MergeStancesForPerson,
	// fact_count grows. Use the LLM's proposed summary as a placeholder; we
	// regenerate it via RefreshStanceMetadata at the end (which sees the actual
	// merged fact list and preserves any tensions the LLM didn't notice when
	// it only had label+summary inputs).
	updatedDst := dst
	updatedDst.Summary = sub.Summary
	updatedDst.ClusterLabel = sub.Label

	// Bring label + summary up to date with the merged fact set: a full rebuild
	// when the facts fit the context, otherwise the absorbed facts (added to dst
	// with a fresh added_at) are folded into dst's current summary. Supersedes
	// the LLM's MergeClusterDecision label/summary, which only saw the two
	// summaries.
	if rErr := stanceManager.RefreshStanceMetadata(ctx, dst.ID); rErr != nil {
		log.Warn().Err(rErr).Int64("dst", dst.ID).Msg("RefreshStanceMetadata failed; dst keeps the merge-decision label/summary")
		// Fall back: persist the merge-decision label/summary so dst isn't left
		// stale (UpdateStanceSummary is the historical behavior). A
		// pre-confirmed subgroup carries none: dst keeps its own, and its
		// absorbed facts stay pending for the next refresh (summarized_at).
		if sub.Label == "" {
			currentByID[dst.ID] = dst
		} else if uErr := storer.UpdateStanceSummary(ctx, dst.ID, sub.Summary, sub.Label); uErr != nil {
			log.Warn().Err(uErr).Int64("dst", dst.ID).Msg("UpdateStanceSummary fallback also failed")
		} else {
			currentByID[dst.ID] = updatedDst
		}
		// The merge is committed; an unavailable LLM still stops the sweep
		// (the absorbed facts stay pending for the next refresh).
		if errors.Is(rErr, llm.ErrUnavailable) {
			return merged, contradictions, analyzeFailures, fmt.Errorf("refreshing merged stance %d: %w", dst.ID, rErr)
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
{"reasoning": "<one short sentence>", "same": true}
or
{"reasoning": "<one short sentence>", "same": false}`, personName, a.Label, a.Summary, b.Label, b.Summary)
}

// SameSubjectByLabel asks the strict same-subject gate about two stances whose
// labels look alike. It is the first of two gates for label-surfaced merge
// candidates; the merge decision still has the last word.
func (sm *StanceManager) SameSubjectByLabel(ctx context.Context, personName string, a, b StanceForMerge) (bool, error) {
	content, err := sm.callJSONLLM(ctx, "You are a political stance topic classifier. Return only valid JSON.", sameSubjectLabelPrompt(personName, a, b), 0.0, 512, llm.TaskStanceMerge)
	if err != nil {
		return false, fmt.Errorf("same-subject gate: %w", err)
	}
	verdict, err := llm.ParseJSONResponse[PairVerdict](content)
	if err != nil {
		return false, fmt.Errorf("same-subject gate: parsing response: %w", err)
	}
	return verdict.Same, nil
}

// lookupFragments asks, for every stance of at most opts.LookupMaxFacts
// facts, which other stance of the person covers its subject
// (SameSubjectLookup), and returns the named pairs as stance ids. It is the
// candidate source for the fragments whose centroid and label sit far from
// their subject's stance. A lookup is memoized (lookupFingerprint): it runs
// again only when the small stance, the stances it could join or the
// rulebook change; with record false (a dry run) the answers are not
// memoized. When the LLM is unavailable no further stance is asked and the
// error is returned; any other failure only skips that stance.
func lookupFragments(ctx context.Context, storer StanceMergeStorer, sm *StanceManager, personName string, docs []StanceDocument, opts StanceMergeOptions, record bool) (pairs [][2]int64, llmCalls, llmFailures int, err error) {
	maxFacts := opts.LookupMaxFacts
	key := MergeRulebookKey(opts)
	fingerprints := map[int64]string{}
	for _, d := range docs {
		if d.FactCount <= maxFacts {
			fingerprints[d.ID] = lookupFingerprint(key, d, docs, maxFacts)
		}
	}
	memo, err := storer.GetStanceLookups(ctx, fingerprints)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("loading the stance lookups: %w", err)
	}
	inDocs := make(map[int64]bool, len(docs))
	for _, d := range docs {
		inDocs[d.ID] = true
	}
	for _, d := range docs {
		if d.FactCount > maxFacts {
			continue
		}
		if named, ok := memo[d.ID]; ok {
			if named != 0 && inDocs[named] {
				pairs = append(pairs, [2]int64{d.ID, named})
			}
			continue
		}
		others := make([]StanceDocument, 0, len(docs)-1)
		for _, o := range docs {
			if o.ID != d.ID {
				others = append(others, o)
			}
		}
		sort.SliceStable(others, func(a, b int) bool { return others[a].FactCount > others[b].FactCount })
		if len(others) > lookupCandidatesCap {
			others = others[:lookupCandidatesCap]
		}
		target := StanceForMerge{ID: d.ID, Label: d.ClusterLabel, Summary: d.Summary}
		id, lErr := sm.SameSubjectLookup(ctx, personName, target, others)
		llmCalls++
		if lErr != nil {
			llmFailures++
			metrics.RecordDecision(metrics.StageStanceMerge, metrics.OutcomeError)
			if errors.Is(lErr, llm.ErrUnavailable) {
				return pairs, llmCalls, llmFailures, lErr
			}
			log.Warn().Err(lErr).Str("person", personName).Int64("stance_id", d.ID).Msg("Stance lookup failed; stance not proposed")
			continue
		}
		if id != 0 {
			log.Info().Str("person", personName).Int64("stance_id", d.ID).Str("label", d.ClusterLabel).Int64("same_as", id).Msg("Stance lookup: same subject as another stance")
			pairs = append(pairs, [2]int64{d.ID, id})
		}
		if record {
			if err := storer.RecordStanceLookup(ctx, d.ID, fingerprints[d.ID], id); err != nil {
				log.Error().Err(err).Int64("stance_id", d.ID).Msg("Recording the stance lookup failed; it runs again next sweep")
			}
		}
	}
	return pairs, llmCalls, llmFailures, nil
}

// lookupFingerprint is the memo key of the lookup of d: the rulebook, the
// label and summary of d, and the ids of the stances it could join (those
// above maxFacts, the ones a lookup lists first). Their labels and counts
// are left out: they change with every refresh, and a fragment names the
// same subject stance across them.
func lookupFingerprint(rulebookKey string, d StanceDocument, docs []StanceDocument, maxFacts int) string {
	var ids []int64
	for _, o := range docs {
		if o.ID != d.ID && o.FactCount > maxFacts {
			ids = append(ids, o.ID)
		}
	}
	sort.Slice(ids, func(a, b int) bool { return ids[a] < ids[b] })
	h := sha256.New()
	fmt.Fprintf(h, "%s\x1f%s\x1f%s\x1f%v", rulebookKey, d.ClusterLabel, d.Summary, ids)
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// sameSubjectLookupPrompt asks which of the person's stances covers the
// subject of target. Rules first, the person's stances next (the same for
// every lookup of the person), the target last (prefix caching).
func sameSubjectLookupPrompt(personName string, target StanceForMerge, others []StanceDocument) string {
	var list strings.Builder
	for _, o := range others {
		fmt.Fprintf(&list, "- id=%d (%d facts): %q\n", o.ID, o.FactCount, o.ClusterLabel)
	}
	return fmt.Sprintf(`A stance is a NEUTRAL SUBJECT AREA of one person: the facts for and against one subject belong to one stance. A small stance may be a fragment of another stance of the person, the same subject recorded under another label. Find that stance.

SAME subject: the same policy field, war, country relation, institution, election or affair, whatever the wording or the direction of the facts. "guerre en Ukraine et diplomatie" belongs to "politique française sur la guerre en Ukraine"; "réforme des retraites de 2023" belongs to "politique des retraites"; "aide à mourir" belongs to "fin de vie et aide à mourir".
NOT the same: a neighbouring field ("politique économique" / "politique sociale"; "immigration" / "identité nationale"), another country or institution ("relations France-Italie" / "relations France-Espagne"), or a wide catch-all that only shares a context word ("élection présidentielle 2027" does not absorb every subject a candidate speaks about).

Person: %s

THE PERSON'S STANCES:
%s
SMALL STANCE:
label: %q
summary: %s

Name the one stance above that covers the same subject, or 0 when none does. Return ONLY strict JSON, the reasoning first:
{"reasoning": "<one short sentence>", "id": 12345}`, personName, list.String(), target.Label, target.Summary)
}

// stanceLookupReply is the answer of sameSubjectLookupPrompt.
type stanceLookupReply struct {
	Reasoning string `json:"reasoning"`
	ID        int64  `json:"id"`
}

// SameSubjectLookup returns the id of the stance of others that covers the
// subject of target, or 0. An id outside others counts as 0.
func (sm *StanceManager) SameSubjectLookup(ctx context.Context, personName string, target StanceForMerge, others []StanceDocument) (int64, error) {
	content, err := sm.callJSONLLM(ctx, "You are a political stance topic classifier. Return only valid JSON.", sameSubjectLookupPrompt(personName, target, others), 0.0, 512, llm.TaskStanceMerge)
	if err != nil {
		return 0, fmt.Errorf("stance lookup: %w", err)
	}
	reply, err := llm.ParseJSONResponse[stanceLookupReply](content)
	if err != nil {
		return 0, fmt.Errorf("stance lookup: parsing response: %w", err)
	}
	for _, o := range others {
		if o.ID == reply.ID {
			return reply.ID, nil
		}
	}
	if reply.ID != 0 {
		log.Warn().Int64("id", reply.ID).Str("person", personName).Msg("Stance lookup named a stance not in the list; ignored")
	}
	return 0, nil
}

// overlapGateFacts is how many facts of each stance the overlap gate reads.
const overlapGateFacts = 5

// sameQuestionsPrompt is the gate of a lookup pair: do the two stances
// overlap enough that a fact of one could confirm or contradict a fact of
// the other? That is what a stance is for: the contradictions are looked
// for inside it. Rules first, the two stances next, the response format last
// (prefix caching).
func sameQuestionsPrompt(personName string, a, b StanceForMerge, factsA, factsB []string) string {
	facts := func(fs []string) string {
		if len(fs) == 0 {
			return "  (no facts loaded)\n"
		}
		var out strings.Builder
		for _, f := range fs {
			fmt.Fprintf(&out, "  - %s\n", f)
		}
		return out.String()
	}
	return fmt.Sprintf(`Two stances of the same person were matched as possibly one subject. A stance gathers the facts on which the person's statements and votes can confirm or contradict each other. Decide whether the two stances OVERLAP: the facts of one answer the same questions as the facts of the other (the same war, reform, budget, negotiation, affair, country relation or institution), so that a fact of one could confirm or contradict a fact of the other.

OVERLAP (answer true): "guerre en Ukraine et diplomatie" and "politique française sur la guerre en Ukraine" (both say what France should do in that war); "fiscalité du tabac" and "fiscalité des ménages et des entreprises" (a tax freeze on tobacco answers the tax question); "cadre financier pluriannuel 2028-2034" and "contribution française au budget de l'Union européenne".

NO OVERLAP (answer false): the facts of one can never confirm or contradict the facts of the other, even under a common heading. A fact on Algeria never contradicts a fact on Israel or Taiwan; "accords de défense bilatéraux" and "sécurité au Proche-Orient"; "racisme et contrôles policiers" and "relations avec les ONG". A stance that gathers many subjects (foreign policy in general, a campaign, the person's image) overlaps a specific stance only when its facts already answer the specific stance's questions.

Judge on the facts, not on the labels.

Person: %s
STANCE A: %q
%sSTANCE B: %q
%s
Return ONLY strict JSON, the reasoning first:
{"reasoning": "<one short sentence>", "same": true}
or
{"reasoning": "<one short sentence>", "same": false}`, personName, a.Label, facts(factsA), b.Label, facts(factsB))
}

// SameQuestions asks the overlap gate about a lookup pair.
func (sm *StanceManager) SameQuestions(ctx context.Context, personName string, a, b StanceForMerge, factsA, factsB []string) (bool, error) {
	content, err := sm.callJSONLLM(ctx, "You are a political stance topic classifier. Return only valid JSON.", sameQuestionsPrompt(personName, a, b, factsA, factsB), 0.0, 512, llm.TaskStanceMerge)
	if err != nil {
		return false, fmt.Errorf("overlap gate: %w", err)
	}
	verdict, err := llm.ParseJSONResponse[PairVerdict](content)
	if err != nil {
		return false, fmt.Errorf("overlap gate: parsing response: %w", err)
	}
	log.Info().Str("person", personName).Str("a", a.Label).Str("b", b.Label).Bool("same", verdict.Same).Str("reasoning", verdict.Reasoning).Msg("Stance overlap gate")
	return verdict.Same, nil
}
