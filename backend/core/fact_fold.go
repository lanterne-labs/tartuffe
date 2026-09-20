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

// SamePositionJudge is the LLM gate deciding whether two statements assert the
// same position on the same matter (FactDedupeAdjudicator.IsSamePosition).
type SamePositionJudge interface {
	IsSamePosition(ctx context.Context, subject, factA, factB string) (bool, error)
}

// FactFoldStorer is the storage view the fold backfill needs.
// *storage.PostgresStorer implements it.
type FactFoldStorer interface {
	MetaFoldStorer
	// GetFoldFactsForPerson returns the person's active (non-duplicate) facts
	// with their embeddings.
	GetFoldFactsForPerson(ctx context.Context, personID int64) ([]FactForFold, error)
	// ListSharedFactIDs returns, among factIDs, the facts mentioned by more
	// than one person.
	ListSharedFactIDs(ctx context.Context, factIDs []int64) (map[int64]bool, error)
	ListContradictionFactPairs(ctx context.Context, personID int64) ([]ContradictionFactPair, error)
	// ListTextSimilarFactPairs returns the pairs among factIDs whose texts
	// have trigram similarity of at least floor, as [smaller id, larger id].
	ListTextSimilarFactPairs(ctx context.Context, factIDs []int64, floor float64) ([][2]int64, error)
	IsMetaFact(ctx context.Context, factID int64) (bool, error)
	GetFactEmbeddingMeta(ctx context.Context, factID int64) (embeddingText, lang, verb string, err error)
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

// FoldClustersForPerson finds same-position clusters among a person's active
// facts: candidate pairs by embedding similarity or by trigram text similarity
// (the embedding slug is nondeterministic across extractions of one quote, so
// near-verbatim restatements often sit outside the embedding window), each
// confirmed by the LLM same-position gate, then union-find. Clusters larger
// than opts.MaxCluster are logged and dropped.
func FoldClustersForPerson(ctx context.Context, storer FactFoldStorer, judge SamePositionJudge, p PersonRecord, opts FoldOptions) ([][]FactForFold, error) {
	facts, err := storer.GetFoldFactsForPerson(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("load facts: %w", err)
	}

	// Shared (co-actor) facts never fold: marking one 'duplicate' is a GLOBAL
	// flag, so folding a shared vote fact under this person's meta would remove
	// it from every other voter's active-fact set. Mirrors the insertion-path
	// guard in FactInserter.
	shared, err := storer.ListSharedFactIDs(ctx, foldFactIDs(facts))
	if err != nil {
		return nil, err
	}
	if len(shared) > 0 {
		kept := facts[:0]
		for _, f := range facts {
			if !shared[f.ID] {
				kept = append(kept, f)
			}
		}
		log.Info().Str("person", p.Name).Int("excluded_shared", len(shared)).
			Msg("Fold: excluded shared co-actor facts from clustering")
		facts = kept
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

	type pairKey struct{ a, b int64 }
	key := func(x, y int64) pairKey {
		if x > y {
			x, y = y, x
		}
		return pairKey{x, y}
	}
	// Hard guard: two facts the detector holds as CONTRADICTORY must never fold
	// into one meta — merging them would erase the contradiction. Deterministic,
	// no LLM. (Their contradiction still repoints onto metas normally when each
	// side folds with its OWN same-position group.)
	contraRows, err := storer.ListContradictionFactPairs(ctx, p.ID)
	if err != nil {
		return nil, fmt.Errorf("load contradictions: %w", err)
	}
	contradicted := map[pairKey]int64{}
	for _, c := range contraRows {
		contradicted[key(c.FactAID, c.FactBID)] = c.ID
	}
	confirmed := map[pairKey]bool{}

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
				// The pair never folds — but if the fold gate considers the two
				// sides the SAME position, the recorded contradiction is almost
				// certainly a false positive (or the gate is wrong): surface it
				// loudly for monitoring. The repoint-changed audit hash already
				// queues such rows for re-judgment at the next audit.
				same, jErr := judge.IsSamePosition(ctx, p.Name, facts[i].Text, facts[j].Text)
				if jErr == nil && same {
					log.Warn().
						Int64("contradiction_id", cid).
						Int64("fact_a", facts[i].ID).
						Int64("fact_b", facts[j].ID).
						Str("person", p.Name).
						Msg("CONTRADICTION UNDER SUSPICION: fold gate judges its two sides as the SAME position — likely detector false positive; next audit will re-judge it")
				}
				continue
			}
			// The LLM position gate is the MANDATORY last decider on membership:
			// embeddings only select candidates (the stance-blind embedding_text
			// puts same-topic-but-different claims above 0.80 too, so no
			// similarity level is trusted on its own).
			same, jErr := judge.IsSamePosition(ctx, p.Name, facts[i].Text, facts[j].Text)
			if jErr != nil {
				log.Warn().Err(jErr).Int64("fact_a", facts[i].ID).Int64("fact_b", facts[j].ID).
					Msg("Same-claim gate failed; treating pair as distinct")
				continue
			}
			if same {
				confirmed[key(facts[i].ID, facts[j].ID)] = true
				union(i, j)
			}
		}
	}

	groups := map[int][]FactForFold{}
	for i := range facts {
		root := find(i)
		groups[root] = append(groups[root], facts[i])
	}
	var clusters [][]FactForFold
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		sort.Slice(g, func(a, b int) bool { return g[a].ID < g[b].ID })
		// COMPLETE-linkage refinement: union-find is single-linkage and can chain
		// two opposing positions through a vague bridge fact (the stance-merge
		// lesson). Keep only members confirmed same-position against EVERY
		// already-kept member, and never across a contradicted pair.
		var clique []FactForFold
		for _, f := range g {
			ok := true
			for _, m := range clique {
				k := key(m.ID, f.ID)
				if !confirmed[k] || contradicted[k] != 0 {
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
		clusters = append(clusters, clique)
	}
	sort.Slice(clusters, func(a, b int) bool { return clusters[a][0].ID < clusters[b][0].ID })
	return clusters, nil
}

// ApplyFoldCluster creates (or reuses) the meta for one cluster and folds every
// member under it: synthesized text, 'duplicate' member links, contradiction
// repoint (collapsing duplicate rows), stance attachment for the meta.
func ApplyFoldCluster(ctx context.Context, storer FactFoldStorer, synth MetaSynthesizer, p PersonRecord, cluster []FactForFold) error {
	// Reuse an existing meta if the cluster contains one (it can absorb the rest).
	var metaID int64
	var members []FactForFold
	for _, f := range cluster {
		isMeta, err := storer.IsMetaFact(ctx, f.ID)
		if err != nil {
			return err
		}
		if isMeta && metaID == 0 {
			metaID = f.ID
		} else {
			members = append(members, f)
		}
	}

	memberTexts := make([]string, 0, len(cluster))
	if metaID != 0 {
		// Reusing an existing meta: its current members must be part of the
		// synthesis too, or their content silently drops out of the meta text.
		existing, err := storer.GetMetaMemberTexts(ctx, metaID)
		if err != nil {
			return err
		}
		memberTexts = append(memberTexts, existing...)
	}
	for _, f := range members {
		memberTexts = append(memberTexts, f.Text)
	}

	rep := cluster[0]
	embeddingText, lang, verb, err := storer.GetFactEmbeddingMeta(ctx, rep.ID)
	if err != nil {
		return err
	}

	text, err := synth.SynthesizeMetaFact(ctx, p.Name, memberTexts, lang)
	if err != nil {
		return fmt.Errorf("synthesize meta text: %w", err)
	}

	if metaID == 0 {
		metaID, err = storer.CreateMetaFact(ctx, p.ID, text, embeddingText, rep.Embedding, lang, verb)
		if err != nil {
			return fmt.Errorf("create meta: %w", err)
		}
		if stanceID, ok, sErr := storer.GetFactStanceID(ctx, p.ID, rep.ID); sErr == nil && ok {
			if aErr := storer.AddFactToStance(ctx, stanceID, metaID); aErr != nil {
				log.Warn().Err(aErr).Int64("meta_fact_id", metaID).Msg("Failed to attach meta to stance")
			}
		}
	} else if err := storer.UpdateMetaFactText(ctx, metaID, text); err != nil {
		return fmt.Errorf("update existing meta %d: %w", metaID, err)
	}

	for _, f := range members {
		if err := storer.MarkFactAsDuplicate(ctx, f.ID, metaID); err != nil {
			return fmt.Errorf("mark member %d: %w", f.ID, err)
		}
		if _, _, err := storer.RepointFactDuplicate(ctx, metaID, f.ID); err != nil {
			return fmt.Errorf("repoint member %d: %w", f.ID, err)
		}
	}
	if err := storer.RefreshPersonScore(ctx, p.ID); err != nil {
		log.Warn().Err(err).Int64("person_id", p.ID).Msg("Failed to refresh fact count")
	}

	log.Info().Str("person", p.Name).Int64("meta_fact_id", metaID).Int("members", len(members)).
		Str("meta_text", util.TruncateString(text, 120)).Msg("Folded cluster into meta-fact")
	return nil
}

func foldFactIDs(facts []FactForFold) []int64 {
	ids := make([]int64, len(facts))
	for i, f := range facts {
		ids[i] = f.ID
	}
	return ids
}
