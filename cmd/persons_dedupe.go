package cmd

import (
	"context"
	"fmt"
	"sort"

	"digital-mirror/backend/core"
	"digital-mirror/backend/storage"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	personsDedupeType      string
	personsDedupeThreshold float64
	personsDedupeApply     bool
)

// personsDedupeCmd finds trigram-similar persons, asks an LLM whether each
// cluster is the same entity, and merges duplicates into a canonical survivor.
var personsDedupeCmd = &cobra.Command{
	Use:   "dedupe",
	Short: "Find and merge duplicate person rows (trigram + LLM adjudication).",
	Long: `Generates fuzzy candidate pairs via pg_trgm, groups them into clusters,
asks an LLM whether each cluster is the SAME real-world entity, and (with
--apply) merges non-canonical members into the canonical survivor.

Dry-run by default — prints what WOULD merge. Pass --apply to mutate.

  dm persons dedupe                          # dry-run, all entity types
  dm persons dedupe --type party             # dry-run, parties only
  dm persons dedupe --threshold 0.5 --apply  # merge above 0.5 similarity

NOTE: trigram similarity only catches same-script variants. Cross-language
duplicates (e.g. "afd" vs "alternative pour l'allemagne") will NOT appear here;
merge those manually with: dm persons merge --from <id> --into <id>`,
	Args:        cobra.NoArgs,
	Annotations: map[string]string{"needs_db": "true"},
	Run:         runPersonsDedupe,
}

func init() {
	personsCmd.AddCommand(personsDedupeCmd)
	personsDedupeCmd.Flags().StringVar(&personsDedupeType, "type", "", "Restrict to one entity_type: human|party|organization. Empty = all.")
	personsDedupeCmd.Flags().Float64Var(&personsDedupeThreshold, "threshold", 0.4, "Minimum trigram similarity (0-1) for candidate pairs.")
	personsDedupeCmd.Flags().BoolVar(&personsDedupeApply, "apply", false, "Actually merge duplicates. Default is dry-run.")
}

func runPersonsDedupe(cmd *cobra.Command, args []string) {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found or error loading.")
	}

	switch personsDedupeType {
	case "", "human", "party", "organization":
	default:
		log.Fatal().Str("type", personsDedupeType).Msg("--type must be one of: human, party, organization (or empty)")
	}

	storer, err := storage.NewPostgresStorerFromEnv(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer")
	}
	defer storer.Close()

	adjudicator, err := core.NewEntityDedupAdjudicatorFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to build entity-dedup adjudicator")
	}

	candidates, err := storer.FindDuplicateCandidates(ctx, personsDedupeType, personsDedupeThreshold)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to find duplicate candidates")
	}
	if len(candidates) == 0 {
		fmt.Println("No candidate duplicate pairs found above threshold.")
		fmt.Println("\nReminder: trigram only catches same-script variants. Cross-language")
		fmt.Println("duplicates need a manual `dm persons merge --from <id> --into <id>`.")
		return
	}

	pairs := make([][2]int64, len(candidates))
	for i, c := range candidates {
		pairs[i] = [2]int64{c.IDA, c.IDB}
	}
	clusters := core.ClusterPairs(pairs)

	var (
		nClusters       = len(clusters)
		nWouldMerge     int
		nMerged         int
		nMergeFailed    int
		nRejected       int
		nClusterErrored int
	)

	fmt.Printf("=== persons dedupe (threshold=%.2f, type=%q, %d candidate pairs, %d clusters) ===\n",
		personsDedupeThreshold, personsDedupeType, len(candidates), nClusters)

	for _, ids := range clusters {
		members, err := storer.GetPersonsForDedup(ctx, ids)
		if err != nil {
			nClusterErrored++
			log.Error().Err(err).Interface("ids", ids).Msg("Failed to load cluster members; skipping")
			continue
		}
		if len(members) < 2 {
			continue
		}

		refs := make([]core.EntityRef, len(members))
		for i, m := range members {
			refs[i] = core.EntityRef{ID: m.ID, Name: m.Name, EntityType: m.EntityType, FactCount: m.FactCount}
		}

		verdict, err := adjudicator.AdjudicateCluster(ctx, refs)
		if err != nil {
			nClusterErrored++
			log.Error().Err(err).Interface("members", refs).Msg("Adjudication failed; skipping cluster")
			continue
		}

		if !verdict.SameEntity {
			nRejected++
			log.Info().
				Interface("members", refs).
				Str("reasoning", verdict.Reasoning).
				Msg("LLM judged cluster as DIFFERENT entities; not merging")
			fmt.Printf("  [skip] %s -- LLM: not the same entity (%s)\n", memberNames(members), verdict.Reasoning)
			continue
		}

		canonical := chooseCanonical(members, verdict.CanonicalID)
		fmt.Printf("  [cluster] canonical=%d (%q)  reasoning=%q\n", canonical.ID, canonical.Name, verdict.Reasoning)
		for _, m := range members {
			if m.ID == canonical.ID {
				continue
			}
			nWouldMerge++
			if !personsDedupeApply {
				fmt.Printf("      would merge %d (%q) -> %d (%q)\n", m.ID, m.Name, canonical.ID, canonical.Name)
				continue
			}
			if err := storer.MergePersons(ctx, m.ID, canonical.ID); err != nil {
				nMergeFailed++
				log.Error().Err(err).Int64("from", m.ID).Int64("into", canonical.ID).Msg("Merge failed")
				continue
			}
			nMerged++
			fmt.Printf("      merged %d (%q) -> %d (%q)\n", m.ID, m.Name, canonical.ID, canonical.Name)
		}
	}

	fmt.Println("\n=== summary ===")
	fmt.Printf("  clusters:           %d\n", nClusters)
	fmt.Printf("  rejected by LLM:    %d\n", nRejected)
	fmt.Printf("  cluster errors:     %d\n", nClusterErrored)
	if personsDedupeApply {
		fmt.Printf("  merged:             %d\n", nMerged)
		fmt.Printf("  merge failed:       %d\n", nMergeFailed)
	} else {
		fmt.Printf("  would merge:        %d\n", nWouldMerge)
		fmt.Println("\nDry run; pass --apply to perform the merges.")
	}
	fmt.Println("\nReminder: trigram only catches same-script variants. Cross-language")
	fmt.Println("duplicates (e.g. AfD vs \"alternative pour l'allemagne\") need a manual")
	fmt.Println("`dm persons merge --from <id> --into <id>`.")
}

// chooseCanonical picks the survivor of a same-entity cluster. Preference order:
// the LLM's canonical_id (if it names a real member), else a curated French
// entity, else the highest fact_count, else the lowest id.
func chooseCanonical(members []storage.DedupMember, llmCanonicalID int64) storage.DedupMember {
	byID := make(map[int64]storage.DedupMember, len(members))
	ids := make([]int64, len(members))
	for i, m := range members {
		byID[m.ID] = m
		ids[i] = m.ID
	}
	if m, ok := byID[llmCanonicalID]; ok {
		return m
	}

	// LLM said same_entity but named a canonical_id that isn't a member of the
	// cluster (or 0). Don't silently discard that — log it before falling back
	// to the heuristic survivor.
	log.Warn().
		Int64("llm_canonical_id", llmCanonicalID).
		Interface("cluster_ids", ids).
		Msg("LLM canonical_id is not a cluster member; falling back to heuristic canonical")

	sorted := make([]storage.DedupMember, len(members))
	copy(sorted, members)
	sort.Slice(sorted, func(i, j int) bool {
		a, b := sorted[i], sorted[j]
		if a.Curated != b.Curated {
			return a.Curated // curated first
		}
		if a.FactCount != b.FactCount {
			return a.FactCount > b.FactCount // more facts first
		}
		return a.ID < b.ID // lowest id first
	})
	return sorted[0]
}

func memberNames(members []storage.DedupMember) string {
	out := ""
	for i, m := range members {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%d:%q", m.ID, m.Name)
	}
	return out
}
