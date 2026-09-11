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
	stancesBackfillPerson string
	stancesBackfillDryRun bool
)

var stancesBackfillCmd = &cobra.Command{
	Use:   "backfill",
	Short: "Backfill stance documents from existing facts",
	Long: `Assigns each existing fact to a stance (creating new stances as needed) using
the same per-fact LLM topic-assignment path as production ingest, then runs the
same LLM-vetted merge sweep the daily pipeline uses (Phase A agglomerative
clustering of centroids + Phase B per-cluster LLM verification + post-merge
contradiction re-detection).

For each person (or specified person):
1. Loads all active facts with embeddings.
2. Deduplicates by exact embedding_text so identical-label facts share one
   ProcessNewFact call (representative + cheap join inserts for duplicates).
3. For each unique label group, calls StanceManager.ProcessNewFact on the
   representative — this runs assignTopic + (when matched) the analyze-vs-stance
   call, mirroring ingest behaviour.
4. After all facts are placed, runs mergeStancesForPersonViaLLM — the same
   merge logic the daily pipeline uses. To tune merge thresholds run
   ` + "`dm stances merge --recall-threshold ...`" + ` afterwards.

Examples:
  # Backfill all persons
  ./dm stances backfill

  # Backfill specific person
  ./dm stances backfill --person "Marine Le Pen"

  # Dry run
  ./dm stances backfill --person "Marine Le Pen" --dry-run`,
	Annotations: map[string]string{"needs_db": "true"},
	RunE:        runStancesBackfill,
}

func init() {
	stancesCmd.AddCommand(stancesBackfillCmd)
	stancesBackfillCmd.Flags().StringVar(&stancesBackfillPerson, "person", "", "Specific person name to backfill (optional)")
	stancesBackfillCmd.Flags().BoolVar(&stancesBackfillDryRun, "dry-run", false, "Show what would be created without writing to database")
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
	StancesCreated          int
	StancesMerged           int
	ContradictionsDetected  int // detected during per-fact ProcessNewFact
	PostMergeContradictions int // detected during post-merge re-analysis (cross-stance pairs)
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
		stats, err := backfillStancesForPerson(ctx, storer, adapter, stanceManager, p.ID, p.Name, stancesBackfillDryRun)
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

	// Filter to persons that actually have facts attached. Skipping zero-fact
	// persons at SQL level avoids paying ~200-500ms of per-person setup (DB
	// roundtrip, validator state, logging) for each of the ~2800 long-tail
	// zero-fact persons in prod that have nothing to backfill anyway.
	return storer.GetPersonsWithFacts(ctx)
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

	// Defer the per-fact refresh — refresh each touched stance ONCE
	// after the loop instead of after every NEW_INFO/CONTRADICTION verdict.
	// For a heavy person (e.g. 40 facts in one stance) this turns 40 refresh
	// LLM calls into 1. The final state is identical because the last refresh
	// folds every fact added since the stance's last refresh (a full rebuild when
	// they fit the context).
	ctxDefer := core.WithDeferRefresh(ctx)
	// Track which stances need an end-of-loop refresh. Only NEW_INFO and
	// CONTRADICTION verdicts would have triggered an inline refresh in the
	// non-deferred path — match that selectively so we don't refresh stances
	// whose only verdicts were NEW_STANCE or DUPLICATE (label is already
	// authoritative).
	stancesNeedingRefresh := make(map[int64]struct{})

	for _, e := range entries {
		rep := e.facts[0]
		res, err := stanceManager.ProcessNewFact(ctxDefer, personID, rep.ID, rep.FactText, rep.Embedding, rep.CanonicalVerb)
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
		if res.Verdict == "NEW_INFO" || res.Verdict == "CONTRADICTION" {
			stancesNeedingRefresh[res.StanceID] = struct{}{}
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

	// Now refresh each touched stance ONCE (we deferred during the loop). For
	// stances that only saw NEW_STANCE / DUPLICATE verdicts this is a no-op
	// per the verdict guard inside handleVerdict, but RefreshStanceMetadata
	// is unconditional — so we only refresh stances that got a NEW_INFO or
	// CONTRADICTION verdict (tracked via stancesNeedingRefresh).
	for stanceID := range stancesNeedingRefresh {
		if err := stanceManager.RefreshStanceMetadata(ctx, stanceID); err != nil {
			log.Warn().Err(err).Int64("stance_id", stanceID).Str("person", personName).Msg("Backfill: end-of-loop refresh failed; stance keeps previous label/summary")
		}
	}

	// LLM-vetted merge sweep — same code path the daily pipeline + `dm stances
	// merge` use. Phase A agglomerative clustering of centroids, Phase B
	// per-cluster LLM verification, then post-merge contradiction re-detection
	// across previously-fragmented facts.
	personInfo := storage.PersonInfo{ID: personID, Name: personName}
	ps, err := mergeStancesForPersonViaLLM(ctx, storer, stanceManager, personInfo, defaultMergeOptions)
	if err != nil {
		log.Error().Err(err).Str("person", personName).Msg("LLM-vetted merge phase failed (stances kept as-is)")
	} else {
		stats.StancesMerged = int(ps.stancesMerged)
		stats.PostMergeContradictions = int(ps.postMergeContradictions)
	}

	log.Info().
		Str("person", personName).
		Int("stances_created", stats.StancesCreated).
		Int("stances_merged", stats.StancesMerged).
		Int("contradictions_detected", stats.ContradictionsDetected).
		Int("post_merge_contradictions", stats.PostMergeContradictions).
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
// Used by applyMergeSubgroup (cmd/stances_merge.go) when recomputing the
// destination centroid after a merge.
type stanceMergeInput struct {
	ID        int64
	FactCount int
	Centroid  []float32
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
