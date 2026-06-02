package cmd

import (
	"context"
	"fmt"

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

// maxConfirmedClusterSize is a defense-in-depth cap: with pairwise adjudication
// a confirmed cluster should stay tiny, so anything larger is suspicious and we
// refuse to bulk-merge it silently.
const maxConfirmedClusterSize = 12

// personsDedupeCmd finds trigram-similar persons, asks an LLM pairwise whether
// each candidate pair is the same entity, clusters the CONFIRMED pairs, and
// merges duplicates into a rule-chosen canonical survivor.
var personsDedupeCmd = &cobra.Command{
	Use:   "dedupe",
	Short: "Find and merge duplicate person rows (trigram + pairwise LLM adjudication).",
	Long: `Generates fuzzy candidate pairs via pg_trgm, asks an LLM whether EACH PAIR is
the SAME real-world entity, clusters only the confirmed pairs (union-find), and
(with --apply) merges non-canonical members into a rule-chosen canonical survivor.

Canonical selection is rule-based (not LLM): a curated French Assemblée entity
always wins; a cluster with two curated entities is skipped; otherwise the
highest fact_count (tie-break lowest id) wins.

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
	personsDedupeCmd.Flags().Float64Var(&personsDedupeThreshold, "threshold", 0.45, "Minimum trigram similarity (0-1) for candidate pairs.")
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

	fmt.Printf("=== persons dedupe (threshold=%.2f, type=%q, %d candidate pairs) ===\n",
		personsDedupeThreshold, personsDedupeType, len(candidates))

	// 1. Adjudicate each candidate pair; keep only confirmed same-entity pairs.
	var (
		confirmedPairs [][2]int64
		nConfirmed     int
		nRejected      int
		nPairErrored   int
	)
	for _, c := range candidates {
		a := core.EntityRef{ID: c.IDA, Name: c.NameA, EntityType: c.EntityTypeA, FactCount: c.FactCountA}
		b := core.EntityRef{ID: c.IDB, Name: c.NameB, EntityType: c.EntityTypeB, FactCount: c.FactCountB}
		verdict, err := adjudicator.AdjudicatePair(ctx, a, b)
		if err != nil {
			nPairErrored++
			log.Error().Err(err).
				Int64("id_a", c.IDA).Str("name_a", c.NameA).
				Int64("id_b", c.IDB).Str("name_b", c.NameB).
				Msg("Pair adjudication failed; skipping pair")
			continue
		}
		if !verdict.Same {
			nRejected++
			log.Debug().
				Int64("id_a", c.IDA).Str("name_a", c.NameA).
				Int64("id_b", c.IDB).Str("name_b", c.NameB).
				Str("reasoning", verdict.Reasoning).
				Msg("LLM judged pair as DIFFERENT entities; not merging")
			continue
		}
		nConfirmed++
		confirmedPairs = append(confirmedPairs, [2]int64{c.IDA, c.IDB})
	}

	// 2. Cluster only the confirmed pairs — chaining through unconfirmed links is
	//    structurally impossible.
	clusters := core.ClusterPairs(confirmedPairs)

	var (
		nClusters       = len(clusters)
		nWouldMerge     int
		nMerged         int
		nMergeFailed    int
		nClusterErrored int
		nClusterSkipped int
	)

	for _, ids := range clusters {
		// Defense-in-depth: never bulk-merge a mega-cluster silently.
		if len(ids) > maxConfirmedClusterSize {
			nClusterSkipped++
			log.Warn().
				Ints64("ids", ids).
				Int("size", len(ids)).
				Int("cap", maxConfirmedClusterSize).
				Msg("Confirmed cluster exceeds safety cap; skipping (will not bulk-merge)")
			fmt.Printf("  [skip] cluster of %d members exceeds cap %d (ids=%v)\n", len(ids), maxConfirmedClusterSize, ids)
			continue
		}

		members, err := storer.GetPersonsForDedup(ctx, ids)
		if err != nil {
			nClusterErrored++
			log.Error().Err(err).Interface("ids", ids).Msg("Failed to load cluster members; skipping")
			continue
		}
		if len(members) < 2 {
			continue
		}

		canonical, ok := storage.ChooseCanonical(members)
		if !ok {
			// Two or more curated members — ChooseCanonical already logged it.
			nClusterSkipped++
			fmt.Printf("  [skip] %s -- 2+ curated entities; refusing to merge real entities\n", memberNames(members))
			continue
		}

		fmt.Printf("  [cluster] canonical=%d (%q)\n", canonical.ID, canonical.Name)
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
	fmt.Printf("  pairs evaluated:    %d\n", len(candidates))
	fmt.Printf("  pairs confirmed:    %d\n", nConfirmed)
	fmt.Printf("  pairs rejected:     %d\n", nRejected)
	fmt.Printf("  pair errors:        %d\n", nPairErrored)
	fmt.Printf("  clusters:           %d\n", nClusters)
	fmt.Printf("  clusters skipped:   %d\n", nClusterSkipped)
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
