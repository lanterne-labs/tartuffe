package cmd

import (
	"context"
	"fmt"
	"os"

	"digital-mirror/backend/core"
	"digital-mirror/backend/deps"
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
	factsFoldCmd.Flags().Float64Var(&factsFoldFloor, "floor", core.DefaultFoldOptions.Floor, "Cosine-similarity floor for same-claim candidates")
	factsFoldCmd.Flags().IntVar(&factsFoldMaxCluster, "max-cluster", core.DefaultFoldOptions.MaxCluster, "Skip clusters larger than this (defense against runaway chaining)")
}

func runFactsFold(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	if err := godotenv.Load(); err != nil {
		log.Debug().Msg(".env file not found, relying on environment variables")
	}
	storer, err := deps.OpenStorer(ctx)
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

	persons, err := personsForCommand(ctx, storer, factsFoldPerson, storer.GetAllPersons)
	if err != nil {
		return err
	}

	opts := core.FoldOptions{Floor: factsFoldFloor, TextFloor: core.DefaultFoldOptions.TextFloor, MaxCluster: factsFoldMaxCluster}
	totalClusters, totalFolded := 0, 0
	for _, p := range persons {
		clusters, err := core.FoldClustersForPerson(ctx, storer, judge, p, opts)
		if err != nil {
			log.Error().Err(err).Str("person", p.Name).Msg("Fold clustering failed; skipping person")
			continue
		}
		for _, cluster := range clusters {
			totalClusters++
			fmt.Fprintf(os.Stderr, "\n%s — cluster of %d:\n", p.Name, len(cluster))
			for _, f := range cluster {
				fmt.Fprintf(os.Stderr, "  [%d] %s\n", f.ID, util.TruncateString(f.Text, 130))
			}
			if !factsFoldApply {
				continue
			}
			if err := core.ApplyFoldCluster(ctx, storer, synth, p, cluster); err != nil {
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
