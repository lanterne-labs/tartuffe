package cmd

import (
	"bufio"
	"context"
	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	pgvector "github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	stancesBackfillPerson         string
	stancesBackfillDryRun         bool
	stancesBackfillMergeThreshold float64
)

var stancesBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Backfill stance documents from existing facts",
	Long: `Assigns each existing fact to a stance (creating new stances as needed) using
the same per-fact LLM topic-assignment path as production ingest, then merges
stance pairs whose normalised cluster_embedding centroids are cosine-similar
above --merge-threshold (default 0.80).

For each person (or specified person):
1. Loads all active facts with embeddings.
2. Deduplicates by exact embedding_text so identical-label facts share one
   ProcessNewFact call (representative + cheap join inserts for duplicates).
3. For each unique label group, calls StanceManager.ProcessNewFact on the
   representative — this runs assignTopic + (when matched) the analyze-vs-stance
   call, mirroring ingest behaviour.
4. After all facts are placed, merges stances whose centroids exceed the cosine
   threshold via UnionFind; the largest stance in each group keeps its summary
   and key facts; centroids are recomputed as a weighted normalised mean.

Examples:
  # Backfill all persons
  ./dm stances backfill

  # Backfill specific person
  ./dm stances backfill --person "Marine Le Pen"

  # Tighter merge (fewer collapses)
  ./dm stances backfill --merge-threshold 0.92

  # Dry run
  ./dm stances backfill --person "Marine Le Pen" --dry-run`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runStancesBackfill,
}

func init() {
	stancesCmd.AddCommand(stancesBackfillCmd)
	stancesBackfillCmd.Flags().StringVar(&stancesBackfillPerson, "person", "", "Specific person name to backfill (optional)")
	stancesBackfillCmd.Flags().BoolVar(&stancesBackfillDryRun, "dry-run", false, "Show what would be created without writing to database")
	stancesBackfillCmd.Flags().Float64Var(&stancesBackfillMergeThreshold, "merge-threshold", 0.80, "Cosine similarity threshold (0-1) for the post-pass centroid merge")
}

// backfillFact holds the data needed for the per-fact ingest loop.
type backfillFact struct {
	ID            int64
	FactText      string
	EmbeddingText string
	CanonicalVerb string
	Embedding     []float32
}

// backfillStats accumulates per-person counters for the rebuild/backfill summary.
type backfillStats struct {
	StancesCreated         int
	StancesMerged          int
	ContradictionsDetected int
}

func runStancesBackfill(cmd *cobra.Command, args []string) error {
	ctx := context.Background()

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		return fmt.Errorf("failed to create storer: %w", err)
	}
	defer storer.Close()

	adapter := storage.NewStanceStorerAdapter(storer)

	var stanceManager *core.StanceManager
	if !stancesBackfillDryRun {
		stanceManager, err = core.NewStanceManagerFromEnv(adapter)
		if err != nil {
			return fmt.Errorf("failed to create stance manager: %w", err)
		}
	}

	persons, err := getPersonsForBackfill(ctx, storer, stancesBackfillPerson)
	if err != nil {
		return err
	}

	log.Info().Int("person_count", len(persons)).Bool("dry_run", stancesBackfillDryRun).Msg("Starting stance backfill")

	var totalCreated, totalMerged, totalContradictions int

	for _, p := range persons {
		stats, err := backfillStancesForPerson(ctx, storer, adapter, stanceManager, p.ID, p.Name, stancesBackfillDryRun, stancesBackfillMergeThreshold)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Failed to backfill stances")
			continue
		}
		totalCreated += stats.StancesCreated
		totalMerged += stats.StancesMerged
		totalContradictions += stats.ContradictionsDetected
	}

	fmt.Println()
	fmt.Println("=== STANCE BACKFILL RESULTS ===")
	fmt.Printf("Persons processed:               %d\n", len(persons))
	fmt.Printf("Stances created:                 %d\n", totalCreated)
	if !stancesBackfillDryRun {
		fmt.Printf("Stances merged (centroid):       %d\n", totalMerged)
		fmt.Printf("Contradictions detected (new):   %d\n", totalContradictions)
	}
	if stancesBackfillDryRun {
		fmt.Println("\n[DRY-RUN] No changes were made to the database")
	}

	return nil
}

func getPersonsForBackfill(ctx context.Context, storer *storage.PostgresStorer, personName string) ([]storage.PersonInfo, error) {
	if personName != "" {
		result := core.ResolveTargetName(ctx, storer, personName)
		if result.Error != nil {
			return nil, fmt.Errorf("resolving person name: %w", result.Error)
		}
		if result.ExactMatch == nil {
			if len(result.FuzzyMatches) > 0 {
				top := result.FuzzyMatches[0]
				return nil, fmt.Errorf("no exact match for %q (best: %s at %.2f). Use exact name", personName, top.Target.Name, top.Score)
			}
			return nil, fmt.Errorf("person %q not found in database", personName)
		}
		return []storage.PersonInfo{
			{ID: *result.ExactMatch.PersonID, Name: result.ExactMatch.Name, EntityType: result.ExactMatch.EntityType},
		}, nil
	}

	return storer.GetAllPersons(ctx)
}

func resolvePersonsFromFile(ctx context.Context, storer *storage.PostgresStorer, path string) ([]storage.PersonInfo, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open persons-file: %w", err)
	}
	defer f.Close()

	var out []storage.PersonInfo
	var unresolved []string
	seen := make(map[int64]bool)
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		name := strings.TrimSpace(scanner.Text())
		if name == "" {
			continue
		}
		result := core.ResolveTargetName(ctx, storer, name)
		if result.Error != nil {
			return nil, fmt.Errorf("resolving %q: %w", name, result.Error)
		}
		if result.ExactMatch == nil || result.ExactMatch.PersonID == nil {
			unresolved = append(unresolved, name)
			continue
		}
		pid := *result.ExactMatch.PersonID
		if seen[pid] {
			continue
		}
		seen[pid] = true
		out = append(out, storage.PersonInfo{
			ID:         pid,
			Name:       result.ExactMatch.Name,
			EntityType: result.ExactMatch.EntityType,
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan persons-file: %w", err)
	}
	if len(unresolved) > 0 {
		log.Warn().Strs("unresolved", unresolved).Int("count", len(unresolved)).Msg("persons-file: some names did not resolve to an exact match; skipping them")
	}
	return out, nil
}

func backfillStancesForPerson(
	ctx context.Context,
	storer *storage.PostgresStorer,
	adapter *storage.StanceStorerAdapter,
	stanceManager *core.StanceManager,
	personID int64,
	personName string,
	dryRun bool,
	mergeThreshold float64,
) (backfillStats, error) {
	var stats backfillStats

	facts, err := loadFactsForBackfill(ctx, storer, personID)
	if err != nil {
		return stats, fmt.Errorf("loading facts: %w", err)
	}

	if len(facts) == 0 {
		log.Info().Str("person", personName).Msg("No facts with embeddings found, skipping")
		return stats, nil
	}

	log.Info().Str("person", personName).Int("facts", len(facts)).Msg("Loaded facts for per-fact stance assignment")

	// Group facts by exact embedding_text so identical labels share one LLM call.
	// The first fact in each group is the representative passed to ProcessNewFact;
	// the rest are joined to the assigned stance via cheap ON CONFLICT inserts.
	groups := clusterFactsByExactLabel(facts)

	// Deterministic order: process groups by the smallest fact ID inside them so
	// semantically related facts surfaced earlier (e.g. clustered by ingest time)
	// have a chance to seed the topic before later contradictions arrive.
	type groupEntry struct {
		label string
		facts []backfillFact
		minID int64
	}
	entries := make([]groupEntry, 0, len(groups))
	for label, gFacts := range groups {
		minID := gFacts[0].ID
		for _, f := range gFacts[1:] {
			if f.ID < minID {
				minID = f.ID
			}
		}
		entries = append(entries, groupEntry{label: label, facts: gFacts, minID: minID})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].minID < entries[j].minID })

	if dryRun || stanceManager == nil {
		for _, e := range entries {
			fmt.Printf("  [DRY-RUN] Would process group %q (%d facts, rep ID=%d)\n", truncateForDisplay(e.label, 80), len(e.facts), e.facts[0].ID)
			stats.StancesCreated++
		}
		return stats, nil
	}

	stanceIDsSeen := make(map[int64]struct{})

	for _, e := range entries {
		rep := e.facts[0]
		res, err := stanceManager.ProcessNewFact(ctx, personID, rep.ID, rep.FactText, rep.Embedding, rep.CanonicalVerb)
		if err != nil {
			log.Error().Err(err).Str("person", personName).Int64("fact_id", rep.ID).Msg("ProcessNewFact failed; skipping group")
			continue
		}
		if res.Verdict == "NEW_STANCE" {
			stats.StancesCreated++
		}
		if res.Verdict == "CONTRADICTION" {
			stats.ContradictionsDetected++
		}
		stanceIDsSeen[res.StanceID] = struct{}{}

		// Attach remaining facts in the exact-label group directly to the assigned
		// stance. We deliberately skip the LLM-analyse path for these — the rep has
		// already established the verdict for this label, and AddFactToStance is
		// idempotent via ON CONFLICT.
		for _, f := range e.facts[1:] {
			if err := adapter.AddFactToStance(ctx, res.StanceID, f.ID); err != nil {
				log.Warn().Err(err).Int64("fact_id", f.ID).Int64("stance_id", res.StanceID).Msg("Failed to attach exact-label dup to stance")
			}
		}
	}

	merged, err := mergeStancesForPersonByCentroid(ctx, storer, personID, mergeThreshold)
	if err != nil {
		log.Error().Err(err).Str("person", personName).Msg("Centroid merge phase failed (stances kept as-is)")
	} else {
		stats.StancesMerged = merged
	}

	log.Info().
		Str("person", personName).
		Int("stances_created", stats.StancesCreated).
		Int("stances_merged", stats.StancesMerged).
		Int("contradictions_detected", stats.ContradictionsDetected).
		Msg("Backfill done for person")

	return stats, nil
}

func loadFactsForBackfill(ctx context.Context, storer *storage.PostgresStorer, personID int64) ([]backfillFact, error) {
	rows, err := storer.Pool().Query(ctx, `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''), COALESCE(f.canonical_verb, ''), f.embedding
		FROM facts f
		JOIN fact_sources fs ON f.id = fs.fact_id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1
		AND NOT EXISTS (
			SELECT 1 FROM fact_relations fr
			WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		)
		GROUP BY f.id
		ORDER BY f.id`, personID)
	if err != nil {
		return nil, fmt.Errorf("querying facts: %w", err)
	}
	defer rows.Close()

	var facts []backfillFact
	for rows.Next() {
		var f backfillFact
		var vec pgvector.Vector
		if err := rows.Scan(&f.ID, &f.FactText, &f.EmbeddingText, &f.CanonicalVerb, &vec); err != nil {
			return nil, fmt.Errorf("scanning fact: %w", err)
		}
		f.Embedding = vec.Slice()
		if len(f.Embedding) == 0 {
			continue
		}
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating facts: %w", err)
	}

	return facts, nil
}

// clusterFactsByExactLabel groups facts by exact embedding_text match.
func clusterFactsByExactLabel(facts []backfillFact) map[string][]backfillFact {
	clusters := make(map[string][]backfillFact)
	for _, fact := range facts {
		label := fact.EmbeddingText
		if label == "" {
			label = fmt.Sprintf("unnamed-fact-%d", fact.ID)
		}
		clusters[label] = append(clusters[label], fact)
	}
	return clusters
}

// stanceMergeInput is the minimal view of a stance the merge logic needs.
// Defined separately from core.StanceDocument so computeMergeGroups can be
// unit-tested without touching the DB or pgvector types.
type stanceMergeInput struct {
	ID        int64
	FactCount int
	Centroid  []float32
}

// mergeStep is one ordered merge produced by computeMergeGroups: src is folded
// into dst, dst's new centroid is the weighted normalised mean of all members.
type mergeStep struct {
	SrcID       int64
	DstID       int64
	NewCentroid []float32
}

// computeMergeGroups produces the set of merges to apply for a person's stances.
// Centroids are assumed L2-normalised so dot product == cosine similarity. Pairs
// with similarity ≥ threshold are unioned (transitive). Within each multi-member
// group the representative is the stance with the largest FactCount (ties broken
// by smallest ID); the new centroid for the representative is the weighted
// normalised mean over all group members.
func computeMergeGroups(stances []stanceMergeInput, threshold float64) []mergeStep {
	n := len(stances)
	if n < 2 {
		return nil
	}

	parent := make([]int, n)
	rank := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(i int) int {
		if parent[i] != i {
			parent[i] = find(parent[i])
		}
		return parent[i]
	}
	union := func(i, j int) {
		ri, rj := find(i), find(j)
		if ri == rj {
			return
		}
		if rank[ri] < rank[rj] {
			ri, rj = rj, ri
		}
		parent[rj] = ri
		if rank[ri] == rank[rj] {
			rank[ri]++
		}
	}

	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			sim := dotProduct(stances[i].Centroid, stances[j].Centroid)
			if sim >= threshold {
				union(i, j)
			}
		}
	}

	groups := make(map[int][]int)
	for i := 0; i < n; i++ {
		r := find(i)
		groups[r] = append(groups[r], i)
	}

	var steps []mergeStep
	// Order groups by their representative's stance ID for deterministic output.
	groupKeys := make([]int, 0, len(groups))
	for k := range groups {
		groupKeys = append(groupKeys, k)
	}
	sort.Ints(groupKeys)

	for _, k := range groupKeys {
		members := groups[k]
		if len(members) < 2 {
			continue
		}
		// Pick representative: largest FactCount, ties broken by smallest ID.
		repIdx := members[0]
		for _, idx := range members[1:] {
			if stances[idx].FactCount > stances[repIdx].FactCount ||
				(stances[idx].FactCount == stances[repIdx].FactCount && stances[idx].ID < stances[repIdx].ID) {
				repIdx = idx
			}
		}
		newCentroid := weightedNormalisedCentroid(stances, members)
		// Emit one merge step per non-rep member, sorted by ID for determinism.
		others := make([]int, 0, len(members)-1)
		for _, idx := range members {
			if idx != repIdx {
				others = append(others, idx)
			}
		}
		sort.Slice(others, func(i, j int) bool { return stances[others[i]].ID < stances[others[j]].ID })
		for _, idx := range others {
			steps = append(steps, mergeStep{
				SrcID:       stances[idx].ID,
				DstID:       stances[repIdx].ID,
				NewCentroid: newCentroid,
			})
		}
	}
	return steps
}

func dotProduct(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// weightedNormalisedCentroid returns Σ(centroid_i × fact_count_i) / Σfact_count_i,
// then L2-normalised. Used as the new centroid for the merge representative.
func weightedNormalisedCentroid(stances []stanceMergeInput, indices []int) []float32 {
	if len(indices) == 0 {
		return nil
	}
	dim := len(stances[indices[0]].Centroid)
	acc := make([]float64, dim)
	var totalWeight float64
	for _, idx := range indices {
		w := float64(stances[idx].FactCount)
		if w <= 0 {
			w = 1
		}
		for i, v := range stances[idx].Centroid {
			acc[i] += float64(v) * w
		}
		totalWeight += w
	}
	if totalWeight == 0 {
		totalWeight = 1
	}
	out := make([]float32, dim)
	var sumSq float64
	for i := range acc {
		v := acc[i] / totalWeight
		out[i] = float32(v)
		sumSq += v * v
	}
	norm := math.Sqrt(sumSq)
	if norm > 0 {
		for i := range out {
			out[i] = float32(float64(out[i]) / norm)
		}
	}
	return out
}

// mergeStancesForPersonByCentroid loads the person's stances, computes which
// pairs cross the cosine threshold, and applies each merge through the storer.
// Returns the number of stances that were merged away (== the number of merge
// steps, since each step removes exactly one src).
func mergeStancesForPersonByCentroid(ctx context.Context, storer *storage.PostgresStorer, personID int64, threshold float64) (int, error) {
	docs, err := storer.GetStanceDocumentsForPerson(ctx, personID)
	if err != nil {
		return 0, fmt.Errorf("loading stances for merge: %w", err)
	}
	if len(docs) < 2 {
		return 0, nil
	}

	inputs := make([]stanceMergeInput, len(docs))
	for i, d := range docs {
		inputs[i] = stanceMergeInput{ID: d.ID, FactCount: d.FactCount, Centroid: d.ClusterEmbedding}
	}

	steps := computeMergeGroups(inputs, threshold)
	if len(steps) == 0 {
		return 0, nil
	}

	merged := 0
	for _, step := range steps {
		if err := storer.MergeStancesForPerson(ctx, step.SrcID, step.DstID, step.NewCentroid); err != nil {
			log.Warn().Err(err).Int64("src", step.SrcID).Int64("dst", step.DstID).Msg("Centroid merge step failed")
			continue
		}
		merged++
	}
	return merged, nil
}
