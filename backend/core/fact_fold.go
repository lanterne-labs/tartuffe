package core

import (
	"context"
	"digital-mirror/backend/util"
	"fmt"
	"sort"

	"github.com/rs/zerolog/log"
)

// FactForFold is an active fact with its embedding, as the fold pass sees it.
type FactForFold struct {
	ID        int64
	Text      string
	Embedding []float32
}

// ContradictionFactPair identifies a recorded contradiction by its two facts.
type ContradictionFactPair struct {
	ID      int64
	FactAID int64
	FactBID int64
}

// FactFoldStorer is the storage view the fold backfill needs.
// *storage.PostgresStorer implements it.
type FactFoldStorer interface {
	GroupStorer
	// GetFoldFactsForPerson returns the person's active (non-duplicate)
	// facts with their embeddings.
	GetFoldFactsForPerson(ctx context.Context, personID int64) ([]FactForFold, error)
	ContradictionPairLister
	// ListTextSimilarFactPairs returns the pairs among factIDs whose texts
	// have trigram similarity of at least floor, as [smaller id, larger id].
	ListTextSimilarFactPairs(ctx context.Context, factIDs []int64, floor float64) ([][2]int64, error)
}

// ContradictionPairLister lists the recorded contradictions of a person.
type ContradictionPairLister interface {
	ListContradictionFactPairs(ctx context.Context, personID int64) ([]ContradictionFactPair, error)
}

// OrderedPair is the key of an unordered fact pair: the smaller id first.
func OrderedPair(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}

// ContradictedPairs returns the recorded contradictions of a person by fact
// pair (OrderedPair), with their ids. The two facts of a recorded
// contradiction never go into one group: the group would erase the
// contradiction.
func ContradictedPairs(ctx context.Context, storer ContradictionPairLister, personID int64) (map[[2]int64]int64, error) {
	rows, err := storer.ListContradictionFactPairs(ctx, personID)
	if err != nil {
		return nil, fmt.Errorf("load contradictions: %w", err)
	}
	out := make(map[[2]int64]int64, len(rows))
	for _, c := range rows {
		out[OrderedPair(c.FactAID, c.FactBID)] = c.ID
	}
	return out, nil
}

// FoldOptions tunes the fold clustering.
type FoldOptions struct {
	Floor      float64 // cosine-similarity floor for same-claim candidates
	TextFloor  float64 // trigram-similarity floor for same-claim candidates (0 = embedding only)
	MaxCluster int     // clusters larger than this are skipped (defense against runaway chaining)
}

// DefaultFoldOptions are the thresholds `dm facts fold` uses. TextFloor is
// the inserter's dedupeTextSimilarityFloor: the two candidate sources of the
// insert-time gate, so the backfill reaches the same pairs.
var DefaultFoldOptions = FoldOptions{Floor: 0.70, TextFloor: dedupeTextSimilarityFloor, MaxCluster: 12}

// FoldCluster is a set of facts that report one occurrence.
type FoldCluster struct {
	Facts []FactForFold
	// Contained: at least one pair is "one fact holds the other's claim plus
	// more". The parent is then the fullest fact.
	Contained bool
}

// FoldClustersForPerson finds same-occurrence clusters among a person's active
// facts: candidate pairs by embedding similarity or by trigram text similarity
// (the embedding slug is nondeterministic across extractions of one quote, so
// near-verbatim restatements often sit outside the embedding window). A
// candidate pair must meet the evidence rule before the LLM occurrence gate
// confirms it (see claim_evidence.go); then union-find. Clusters larger than
// opts.MaxCluster are logged and dropped.
func FoldClustersForPerson(ctx context.Context, storer FactFoldStorer, judge SameOccurrenceJudge, p PersonRecord, opts FoldOptions) ([]FoldCluster, error) {
	rule, err := groupEvidenceRule()
	if err != nil {
		return nil, err
	}
	facts, err := storer.GetFoldFactsForPerson(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("load facts: %w", err)
	}

	if len(facts) < 2 {
		return nil, nil
	}

	parent := make([]int, len(facts))
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		if parent[x] != x {
			parent[x] = find(parent[x])
		}
		return parent[x]
	}
	union := func(a, b int) { parent[find(a)] = find(b) }

	type pairKey = [2]int64
	key := OrderedPair
	// Hard guard: two facts the detector holds as CONTRADICTORY must never fold
	// into one meta — merging them would erase the contradiction. Deterministic,
	// no LLM. (Their contradiction still repoints onto metas normally when each
	// side folds with its OWN group.)
	contradicted, err := ContradictedPairs(ctx, storer, p.ID)
	if err != nil {
		return nil, err
	}
	confirmed := map[pairKey]OccurrenceVerdict{}

	textPairs := map[pairKey]bool{}
	if opts.TextFloor > 0 {
		pairs, err := storer.ListTextSimilarFactPairs(ctx, foldFactIDs(facts), opts.TextFloor)
		if err != nil {
			return nil, fmt.Errorf("load text-similar pairs: %w", err)
		}
		for _, pr := range pairs {
			textPairs[key(pr[0], pr[1])] = true
		}
	}

	// COSINE distances, matching the insertion-path thresholds (FindSimilarActiveFacts
	// uses cosine). NB the pgvector `<->` operator is L2, not cosine — using L2 here
	// once made the 0.70 floor behave like cosine 0.955 and fold only near-verbatim
	// pairs (caught by the 2026-07-29 prod audit).
	floorDist := 1.0 - opts.Floor
	var candidates, contradictedCandidates [][2]int
	for i := 0; i < len(facts); i++ {
		for j := i + 1; j < len(facts); j++ {
			if !textPairs[key(facts[i].ID, facts[j].ID)] {
				if len(facts[i].Embedding) == 0 || len(facts[j].Embedding) == 0 {
					continue
				}
				if util.CosineDistance(facts[i].Embedding, facts[j].Embedding) > floorDist {
					continue
				}
			}
			if cid := contradicted[key(facts[i].ID, facts[j].ID)]; cid != 0 {
				// The pair never folds; if it also passes the evidence rule
				// and the gate, the log below reports it.
				contradictedCandidates = append(contradictedCandidates, [2]int{i, j})
				continue
			}
			candidates = append(candidates, [2]int{i, j})
		}
	}

	all := append(append([][2]int{}, candidates...), contradictedCandidates...)
	pairIDs := make([][2]int64, len(all))
	for n, c := range all {
		pairIDs[n] = [2]int64{facts[c[0]].ID, facts[c[1]].ID}
	}
	evidenceRows, err := storer.ListGroupEvidencePairs(ctx, pairIDs, rule)
	if err != nil {
		return nil, fmt.Errorf("load group-evidence pairs: %w", err)
	}
	evidence := map[pairKey]bool{}
	for _, pr := range evidenceRows {
		evidence[key(pr[0], pr[1])] = true
	}
	sources, err := storer.LoadClaimSources(ctx, foldFactIDs(facts), rule.SourceTypes)
	if err != nil {
		return nil, fmt.Errorf("load claim sources: %w", err)
	}
	ask := func(i, j int) (OccurrenceVerdict, error) {
		return judge.IsSameOccurrence(ctx,
			ClaimEvidence{Text: facts[i].Text, Sources: sources[facts[i].ID]},
			ClaimEvidence{Text: facts[j].Text, Sources: sources[facts[j].ID]})
	}
	for _, c := range candidates {
		i, j := c[0], c[1]
		if !evidence[key(facts[i].ID, facts[j].ID)] {
			continue
		}
		// The LLM gate is the MANDATORY last decider on membership:
		// embeddings only select candidates (the stance-blind embedding_text
		// puts same-topic-but-different claims above 0.80 too, so no
		// similarity level is trusted on its own).
		verdict, jErr := ask(i, j)
		if jErr != nil {
			log.Warn().Err(jErr).Int64("fact_a", facts[i].ID).Int64("fact_b", facts[j].ID).
				Msg("Occurrence gate failed; treating pair as distinct")
			continue
		}
		if verdict.Groupable() {
			confirmed[key(facts[i].ID, facts[j].ID)] = verdict
			union(i, j)
		}
	}
	// A recorded contradiction whose two sides the gate reads as one
	// occurrence is almost certainly a false positive (or the gate is wrong):
	// surface it loudly for monitoring. The repoint-changed audit hash already
	// queues such rows for re-judgment at the next audit.
	for _, c := range contradictedCandidates {
		i, j := c[0], c[1]
		if !evidence[key(facts[i].ID, facts[j].ID)] {
			continue
		}
		if verdict, jErr := ask(i, j); jErr == nil && verdict.Groupable() {
			log.Warn().
				Int64("contradiction_id", contradicted[key(facts[i].ID, facts[j].ID)]).
				Int64("fact_a", facts[i].ID).
				Int64("fact_b", facts[j].ID).
				Str("person", p.Name).
				Msg("CONTRADICTION UNDER SUSPICION: the grouping gate reads its two sides as one occurrence — likely detector false positive; next audit will re-judge it")
		}
	}

	groups := map[int][]FactForFold{}
	for i := range facts {
		root := find(i)
		groups[root] = append(groups[root], facts[i])
	}
	var clusters []FoldCluster
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		sort.Slice(g, func(a, b int) bool { return g[a].ID < g[b].ID })
		// COMPLETE-linkage refinement: union-find is single-linkage and can chain
		// two opposing positions through a vague bridge fact (the stance-merge
		// lesson). Keep only members the gate confirmed against EVERY
		// already-kept member, and never across a contradicted pair.
		var clique []FactForFold
		for _, f := range g {
			ok := true
			for _, m := range clique {
				k := key(m.ID, f.ID)
				if !confirmed[k].Groupable() || contradicted[k] != 0 {
					ok = false
					break
				}
			}
			if ok {
				clique = append(clique, f)
			}
		}
		if len(clique) < 2 {
			continue
		}
		if len(clique) > opts.MaxCluster {
			log.Warn().Str("person", p.Name).Int("size", len(clique)).
				Msg("Skipping oversized fold cluster (raise --max-cluster to include)")
			continue
		}
		cluster := FoldCluster{Facts: clique}
		for a := range clique {
			for b := a + 1; b < len(clique); b++ {
				if confirmed[key(clique[a].ID, clique[b].ID)] == OccurrenceContained {
					cluster.Contained = true
				}
			}
		}
		clusters = append(clusters, cluster)
	}
	sort.Slice(clusters, func(a, b int) bool { return clusters[a].Facts[0].ID < clusters[b].Facts[0].ID })
	return clusters, nil
}

// ApplyFoldCluster groups one cluster of same-occurrence facts: the most
// concrete becomes the parent (the fullest, for a contained cluster, so that
// no claim is hidden) and every other fact (with any members it already had)
// joins its group — contradictions, stance slots and key facts follow the
// parent (storage.MergeFactIntoGroup). It returns the parent.
func ApplyFoldCluster(ctx context.Context, storer FactFoldStorer, p PersonRecord, c FoldCluster) (int64, error) {
	cluster := c.Facts
	var parent int64
	if c.Contained {
		best := 0
		for i := range cluster {
			if fullerFact(cluster[best].Text, cluster[i].Text) == 1 {
				best = i
			}
		}
		parent = cluster[best].ID
	} else {
		var err error
		parent, err = storer.MostConcreteFact(ctx, foldFactIDs(cluster))
		if err != nil {
			return 0, err
		}
	}
	for _, f := range cluster {
		if f.ID == parent {
			continue
		}
		if err := storer.MergeFactIntoGroup(ctx, f.ID, parent); err != nil {
			return 0, fmt.Errorf("grouping fact %d under %d: %w", f.ID, parent, err)
		}
	}
	if err := storer.RefreshPersonScore(ctx, p.ID); err != nil {
		log.Warn().Err(err).Int64("person_id", p.ID).Msg("Failed to refresh fact count")
	}
	log.Info().Str("person", p.Name).Int64("parent_fact_id", parent).Int("members", len(cluster)-1).
		Msg("Grouped one-occurrence cluster")
	return parent, nil
}

func foldFactIDs(facts []FactForFold) []int64 {
	ids := make([]int64, len(facts))
	for i, f := range facts {
		ids[i] = f.ID
	}
	return ids
}
