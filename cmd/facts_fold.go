package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"digital-mirror/backend/util"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	factsFoldPerson     string
	factsFoldApply      bool
	factsFoldFloor      float64
	factsFoldMaxCluster int
)

var factsFoldCmd = &cobra.Command{
	Use:   "fold",
	Short: "Fold near-identical same-claim facts under LLM-synthesized meta-facts.",
	Long: `Backfill pass over existing facts: clusters same-claim facts of each person
(embedding similarity + the same verb-aware / LLM gates as insertion dedup),
creates one meta-fact per cluster (raw facts are kept as members), repoints
their contradictions onto the meta (collapsing duplicate rows), and recomputes
scores. Insertion-time folding handles new facts; this command cleans up the
piles that accumulated before it existed (e.g. 20 copies of the same
innocence-vs-conviction contradiction).

READ-ONLY by default: prints the clusters it would fold. Pass --apply to write.`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runFactsFold,
}

func init() {
	factsCmd.AddCommand(factsFoldCmd)
	factsFoldCmd.Flags().StringVar(&factsFoldPerson, "person", "", "Restrict to one person (default: all persons)")
	factsFoldCmd.Flags().BoolVar(&factsFoldApply, "apply", false, "Apply the folds (default is a read-only report)")
	factsFoldCmd.Flags().Float64Var(&factsFoldFloor, "floor", 0.70, "Cosine-similarity floor for same-claim candidates")
	factsFoldCmd.Flags().IntVar(&factsFoldMaxCluster, "max-cluster", 12, "Skip clusters larger than this (defense against runaway chaining)")
}

func runFactsFold(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}
	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL storer: %w", err)
	}
	defer storer.Close()

	judge, err := core.NewFactDedupeAdjudicatorFromEnv()
	if err != nil {
		return fmt.Errorf("same-claim adjudicator required for folding: %w", err)
	}
	synth, err := core.NewLLMMetaSynthesizerFromEnv()
	if err != nil {
		return fmt.Errorf("meta synthesizer required for folding: %w", err)
	}

	persons, err := foldTargetPersons(ctx, storer)
	if err != nil {
		return err
	}

	totalClusters, totalFolded := 0, 0
	for _, p := range persons {
		clusters, err := foldClustersForPerson(ctx, storer, judge, p)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Fold clustering failed; skipping person")
			continue
		}
		for _, cluster := range clusters {
			if len(cluster) > factsFoldMaxCluster {
				log.Warn().Str("person", p.Name).Int("size", len(cluster)).
					Msg("Skipping oversized fold cluster (raise --max-cluster to include)")
				continue
			}
			totalClusters++
			fmt.Fprintf(os.Stderr, "\n%s — cluster of %d:\n", p.Name, len(cluster))
			for _, f := range cluster {
				fmt.Fprintf(os.Stderr, "  [%d] %s\n", f.ID, truncateForDisplay(f.Text, 130))
			}
			if !factsFoldApply {
				continue
			}
			if err := applyFoldCluster(ctx, storer, synth, p, cluster); err != nil {
				log.Error().Err(err).Str("person", p.Name).Msg("Failed to apply fold cluster")
				continue
			}
			totalFolded++
		}
	}

	if factsFoldApply && totalFolded > 0 {
		// Folding changes active-fact counts and collapses contradictions; settle
		// every affected score in one deterministic pass.
		if _, err := storer.RecomputeAllHonestyScores(ctx, false); err != nil {
			log.Error().Err(err).Msg("Failed to recompute honesty scores after folding")
		}
	}
	if factsFoldApply {
		fmt.Fprintf(os.Stderr, "\nFolded %d/%d clusters into meta-facts.\n", totalFolded, totalClusters)
	} else {
		fmt.Fprintf(os.Stderr, "\n[DRY RUN] %d fold clusters found. Re-run with --apply to write.\n", totalClusters)
	}
	return nil
}

func foldTargetPersons(ctx context.Context, storer *storage.PostgresStorer) ([]storage.PersonInfo, error) {
	if factsFoldPerson != "" {
		name := util.NormalizeString(factsFoldPerson)
		id, err := storer.GetPersonIDByName(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("person %q not found: %w", factsFoldPerson, err)
		}
		return []storage.PersonInfo{{ID: id, Name: name}}, nil
	}
	persons, err := storer.GetAllPersons(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list persons: %w", err)
	}
	return persons, nil
}

// foldClustersForPerson finds same-claim clusters among a person's active facts:
// candidate pairs by embedding similarity, confirmed by the same verb-aware
// tier-1 rule or the LLM same-claim gate used at insertion, then union-find.
func foldClustersForPerson(ctx context.Context, storer *storage.PostgresStorer, judge *core.FactDedupeAdjudicator, p storage.PersonInfo) ([][]storage.FactWithEmbedding, error) {
	facts, err := storer.GetFactsWithEmbeddingsForPerson(ctx, p.ID)
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

	type pairKey struct{ a, b int64 }
	key := func(x, y int64) pairKey {
		if x > y {
			x, y = y, x
		}
		return pairKey{x, y}
	}
	// Hard guard: two facts the detector holds as CONTRADICTORY must never fold
	// — merging them would erase the contradiction. Deterministic, no LLM.
	contraRows, err := storer.ListContradictions(ctx, &p.ID, false, 0)
	if err != nil {
		return nil, fmt.Errorf("load contradictions: %w", err)
	}
	contradicted := map[pairKey]bool{}
	for _, c := range contraRows {
		contradicted[key(c.FactAID, c.FactBID)] = true
	}
	confirmed := map[pairKey]bool{}

	// COSINE distances, matching the insertion-path thresholds (FindSimilarActiveFacts
	// uses cosine). NB the pgvector `<->` operator is L2, not cosine — using L2 here
	// once made the 0.70 floor behave like cosine 0.955 and fold only near-verbatim
	// pairs (caught by the 2026-07-29 prod audit).
	floorDist := 1.0 - factsFoldFloor
	for i := 0; i < len(facts); i++ {
		if len(facts[i].Embedding) == 0 {
			continue
		}
		for j := i + 1; j < len(facts); j++ {
			if len(facts[j].Embedding) == 0 {
				continue
			}
			d := cosineDistance(facts[i].Embedding, facts[j].Embedding)
			if d > floorDist {
				continue
			}
			if contradicted[key(facts[i].ID, facts[j].ID)] {
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

	groups := map[int][]storage.FactWithEmbedding{}
	for i := range facts {
		root := find(i)
		groups[root] = append(groups[root], facts[i])
	}
	var clusters [][]storage.FactWithEmbedding
	for _, g := range groups {
		if len(g) < 2 {
			continue
		}
		sort.Slice(g, func(a, b int) bool { return g[a].ID < g[b].ID })
		// COMPLETE-linkage refinement: union-find is single-linkage and can chain
		// two opposing positions through a vague bridge fact (the stance-merge
		// lesson). Keep only members confirmed same-position against EVERY
		// already-kept member, and never across a contradicted pair.
		var clique []storage.FactWithEmbedding
		for _, f := range g {
			ok := true
			for _, m := range clique {
				k := key(m.ID, f.ID)
				if !confirmed[k] || contradicted[k] {
					ok = false
					break
				}
			}
			if ok {
				clique = append(clique, f)
			}
		}
		if len(clique) >= 2 {
			clusters = append(clusters, clique)
		}
	}
	sort.Slice(clusters, func(a, b int) bool { return clusters[a][0].ID < clusters[b][0].ID })
	return clusters, nil
}

// applyFoldCluster creates (or reuses) the meta for one cluster and folds every
// member under it: synthesized text, 'duplicate' member links, contradiction
// repoint (collapsing duplicate rows), stance attachment for the meta.
func applyFoldCluster(ctx context.Context, storer *storage.PostgresStorer, synth core.MetaSynthesizer, p storage.PersonInfo, cluster []storage.FactWithEmbedding) error {
	// Reuse an existing meta if the cluster contains one (it can absorb the rest).
	var metaID int64
	var members []storage.FactWithEmbedding
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
		if stanceID, ok, sErr := storer.GetFactStanceID(ctx, rep.ID); sErr == nil && ok {
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
	if err := storer.RefreshPersonFactCount(ctx, p.ID); err != nil {
		log.Warn().Err(err).Int64("person_id", p.ID).Msg("Failed to refresh fact count")
	}

	log.Info().Str("person", p.Name).Int64("meta_fact_id", metaID).Int("members", len(members)).
		Str("meta_text", truncateForDisplay(text, 120)).Msg("Folded cluster into meta-fact")
	return nil
}

// cosineDistance returns 1 - cosine_similarity. Fact embeddings are stored
// L2-normalized (verified: <#> self-product = -1), so the dot product IS the
// cosine similarity.
func cosineDistance(a, b []float32) float64 {
	var dot float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
	}
	return 1.0 - dot
}
