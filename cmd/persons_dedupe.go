package cmd

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	personsDedupeType      string
	personsDedupeThreshold float64
	personsDedupeApply     bool
)

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
	personsDedupeCmd.Flags().Float64Var(&personsDedupeThreshold, "threshold", core.DefaultPersonDedupeOptions.Threshold, "Minimum trigram similarity (0-1) for candidate pairs.")
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

	storer, err := deps.OpenStorer(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create PostgreSQL storer")
	}
	defer storer.Close()

	adjudicator, err := core.NewEntityDedupAdjudicatorFromEnv()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to build entity-dedup adjudicator")
	}

	res, err := core.DedupePersons(ctx, storer, adjudicator, core.PersonDedupeOptions{
		EntityType: personsDedupeType,
		Threshold:  personsDedupeThreshold,
		Apply:      personsDedupeApply,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("Persons dedupe failed")
	}
	if len(res.Candidates) == 0 {
		fmt.Println("No candidate duplicate pairs found above threshold.")
		fmt.Println("\nReminder: trigram only catches same-script variants. Cross-language")
		fmt.Println("duplicates need a manual `dm persons merge --from <id> --into <id>`.")
		return
	}

	fmt.Printf("=== persons dedupe (threshold=%.2f, type=%q, %d candidate pairs) ===\n",
		personsDedupeThreshold, personsDedupeType, len(res.Candidates))

	for _, c := range res.Clusters {
		switch c.SkipReason {
		case core.PersonClusterSkipCap:
			fmt.Printf("  [skip] cluster of %d members exceeds cap %d (ids=%v)\n", len(c.IDs), res.MaxClusterSize, c.IDs)
		case core.PersonClusterSkipCurated:
			fmt.Printf("  [skip] %s -- 2+ curated entities; refusing to merge real entities\n", memberNames(c.Members))
		default:
			fmt.Printf("  [cluster] canonical=%d (%q)\n", c.Canonical.ID, c.Canonical.Name)
			for _, m := range c.Merges {
				switch {
				case m.Err != nil:
					// Logged by core; nothing to report as done.
				case m.Applied:
					fmt.Printf("      merged %d (%q) -> %d (%q)\n", m.From.ID, m.From.Name, m.Into.ID, m.Into.Name)
				default:
					fmt.Printf("      would merge %d (%q) -> %d (%q)\n", m.From.ID, m.From.Name, m.Into.ID, m.Into.Name)
				}
			}
		}
	}

	fmt.Println("\n=== summary ===")
	fmt.Printf("  pairs evaluated:    %d\n", len(res.Candidates))
	fmt.Printf("  pairs confirmed:    %d\n", res.PairsConfirmed)
	fmt.Printf("  pairs rejected:     %d\n", res.PairsRejected)
	fmt.Printf("  pair errors:        %d\n", res.PairErrors)
	fmt.Printf("  clusters:           %d\n", len(res.Clusters)+res.ClusterErrors)
	fmt.Printf("  clusters skipped:   %d\n", res.ClustersSkipped)
	fmt.Printf("  cluster errors:     %d\n", res.ClusterErrors)
	if personsDedupeApply {
		fmt.Printf("  merged:             %d\n", res.Merged)
		fmt.Printf("  merge failed:       %d\n", res.MergeFailed)
	} else {
		fmt.Printf("  would merge:        %d\n", res.WouldMerge)
		fmt.Println("\nDry run; pass --apply to perform the merges.")
	}
	fmt.Println("\nReminder: trigram only catches same-script variants. Cross-language")
	fmt.Println("duplicates (e.g. AfD vs \"alternative pour l'allemagne\") need a manual")
	fmt.Println("`dm persons merge --from <id> --into <id>`.")
}

func memberNames(members []core.PersonDedupMember) string {
	out := ""
	for i, m := range members {
		if i > 0 {
			out += ", "
		}
		out += fmt.Sprintf("%d:%q", m.ID, m.Name)
	}
	return out
}
